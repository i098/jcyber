package traffic

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mproxy "github.com/lqqyt2423/go-mitmproxy/proxy"
)

const testChain = `{"ws":"http://u:p@ws.test:80","geo":"http://g.test:9000","sticky":"http://u:p@g.test:10000"}`

func chainReq(host string) *http.Request {
	u, _ := url.Parse("https://" + host + "/x")
	return &http.Request{URL: u, Host: host, Method: http.MethodGet}
}

func pick(t *testing.T, tr *Traffic, host string) string {
	t.Helper()
	u := tr.pickUpstream(chainReq(host))
	if u == nil {
		return ""
	}
	return u.Host
}

// TestEgressChainOrder covers the fallback order: webshare first, geonode once
// webshare is parked, sticky only for a host that was challenged.
func TestEgressChainOrder(t *testing.T) {
	tr := &Traffic{}
	if got := pick(t, tr, "a.test"); got != "" {
		t.Fatalf("no chain must fall back to the single upstream (nil), got %q", got)
	}
	if err := tr.SetEgressChain(testChain); err != nil {
		t.Fatalf("SetEgressChain: %v", err)
	}
	if got := pick(t, tr, "a.test"); got != "ws.test:80" {
		t.Fatalf("default egress = %q, want webshare", got)
	}

	e := tr.egress.Load()
	e.parkWS("quota", time.Minute)
	if got := pick(t, tr, "a.test"); got != "g.test:9000" {
		t.Fatalf("parked webshare must fall back to geonode, got %q", got)
	}

	e.markSticky("c.test", "challenge")
	if got := pick(t, tr, "c.test"); got != "g.test:10000" {
		t.Fatalf("challenged host = %q, want sticky", got)
	}
	if got := pick(t, tr, "a.test"); got != "g.test:9000" {
		t.Fatalf("other hosts must keep the rotating fallback, got %q", got)
	}

	user := e.sticky.User.Username()
	if !strings.Contains(user, "-session-") || !strings.Contains(user, "-lifetime-60") {
		t.Fatalf("sticky username is not a pinned session: %q", user)
	}

	e.stickyHosts["c.test"] = time.Now().Add(-time.Second).Unix()
	if got := pick(t, tr, "c.test"); got != "g.test:9000" {
		t.Fatalf("expired sticky pin must release the host, got %q", got)
	}

	if err := tr.SetEgressChain(""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got := pick(t, tr, "a.test"); got != "" {
		t.Fatalf("cleared chain must fall back to the single upstream, got %q", got)
	}
}

func chainFlow(status int, ctype, body string) *mproxy.Flow {
	u, _ := url.Parse("https://ch.test/")
	return &mproxy.Flow{
		Request:  &mproxy.Request{Method: http.MethodGet, URL: u},
		Response: &mproxy.Response{StatusCode: status, Header: http.Header{"Content-Type": {ctype}}, Body: []byte(body)},
	}
}

// TestNoteEgress covers the two reactions the chain depends on: a proxy quota
// answer parks webshare, a Cloudflare challenge pins that host to sticky.
func TestNoteEgress(t *testing.T) {
	tr := &Traffic{}
	if err := tr.SetEgressChain(testChain); err != nil {
		t.Fatal(err)
	}

	tr.noteEgress(chainFlow(407, "text/plain", "quota exceeded"))
	if tr.egress.Load().wsAvailable() {
		t.Fatal("HTTP 407 from the proxy must park webshare")
	}

	if err := tr.SetEgressChain(testChain); err != nil {
		t.Fatal(err)
	}
	tr.noteEgress(chainFlow(403, "text/html; charset=UTF-8", "<html><title>Just a moment...</title></html>"))
	if !tr.egress.Load().stickied("ch.test") {
		t.Fatal("a Cloudflare challenge must pin the host to the sticky session")
	}

	if err := tr.SetEgressChain(testChain); err != nil {
		t.Fatal(err)
	}
	tr.noteEgress(chainFlow(403, "text/html", "<html>forbidden</html>"))
	if tr.egress.Load().stickied("ch.test") {
		t.Fatal("a plain 403 page must not pin sticky")
	}
	tr.noteEgress(chainFlow(403, "application/json", `{"err":"Just a moment"}`))
	if tr.egress.Load().stickied("ch.test") {
		t.Fatal("a non-HTML body must not pin sticky")
	}
}

// TestNoteEgressError: an exhausted provider usually resets the connection
// instead of answering 407, so a transport error while webshare is in use must
// park it too — but only briefly, and never when a response actually arrived.
func TestNoteEgressError(t *testing.T) {
	tr := &Traffic{}
	if err := tr.SetEgressChain(testChain); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse("https://ch.test/")
	noResp := &mproxy.Flow{Request: &mproxy.Request{Method: http.MethodGet, URL: u}}
	tr.noteEgressError(noResp, errors.New("connection reset by peer"))
	e := tr.egress.Load()
	if e.wsAvailable() {
		t.Fatal("transport error must park webshare")
	}
	if left := time.Until(time.Unix(e.parkedUntil.Load(), 0)); left > 3*time.Minute {
		t.Fatalf("error park = %s, want the short window", left)
	}

	if err := tr.SetEgressChain(testChain); err != nil {
		t.Fatal(err)
	}
	tr.noteEgressError(chainFlow(200, "text/html", "ok"), errors.New("dial failed"))
	if !tr.egress.Load().wsAvailable() {
		t.Fatal("a flow that got a response must not park webshare")
	}
}

// TestProbeWS: the probe is what catches a provider that is out of quota but
// answers with a reset (no flow, so the response hooks never see it).
func TestProbeWS(t *testing.T) {
	var dead atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if dead.Load() {
			w.WriteHeader(http.StatusProxyAuthRequired)
			return
		}
		_, _ = w.Write([]byte("1.2.3.4"))
	}))
	defer srv.Close()

	tr := &Traffic{}
	if err := tr.SetEgressChain(`{"ws":"` + srv.URL + `","geo":"http://g.test:9000"}`); err != nil {
		t.Fatal(err)
	}
	e := tr.egress.Load()
	dead.Store(true)
	e.probeWS()
	if e.wsAvailable() {
		t.Fatal("a 407 probe must park webshare")
	}
	dead.Store(false)
	e.probeWS()
	if !e.wsAvailable() {
		t.Fatal("a healthy probe must unpark webshare again")
	}
}
