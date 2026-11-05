package traffic

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	mproxy "github.com/lqqyt2423/go-mitmproxy/proxy"
)

// egressChain is the fallback order for target traffic:
//
//	ws (rotating residential) -> geo (rotating) -> sticky (pinned session)
//
// webshare is the default and is parked for wsRetry after it reports exhaustion
// (402/407), so the very next request leaves through geonode instead. A host that
// answers a Cloudflare challenge is switched to the sticky session, because
// cf_clearance is bound to IP+UA and dies on a rotating exit.
type egressChain struct {
	ws, geo, sticky *url.URL
	wsRetry         time.Duration
	stickyTTL       time.Duration

	parkedUntil atomic.Int64 // unix seconds, 0 = available
	parkMu      sync.Mutex

	stop        chan struct{}
	stickyMu    sync.Mutex
	stickyHosts map[string]int64 // host -> unix expiry
	stickySess  string           // current session id (whole chain shares one pin)
}

var challengeRE = regexp.MustCompile(`(?i)just a moment|managed challenge|cf-challenge|challenge-platform|attention required|verify you are human|enable javascript and cookies`)

// egressChainJSON is the persisted shape of the chain (settings key egress_chain).
type egressChainJSON struct {
	WS     string `json:"ws"`
	Geo    string `json:"geo"`
	Sticky string `json:"sticky"`
}

// SetEgressChain installs the fallback chain. An empty raw string (or one with
// no ws/geo) clears it, restoring the single global proxy / direct dialing.
// Hot-swappable: the next request picks up the new chain.
func (t *Traffic) SetEgressChain(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		t.egress.Store(nil)
		return nil
	}
	var spec egressChainJSON
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		return fmt.Errorf("egress chain: %w", err)
	}
	if strings.TrimSpace(spec.WS) == "" || strings.TrimSpace(spec.Geo) == "" {
		return fmt.Errorf("egress chain needs both ws and geo proxy URLs")
	}
	e := &egressChain{wsRetry: 10 * time.Minute, stickyTTL: 30 * time.Minute, stickyHosts: map[string]int64{}, stop: make(chan struct{})}
	var err error
	if e.ws, err = ValidateProxyURL(spec.WS); err != nil {
		return fmt.Errorf("egress chain ws: %w", err)
	}
	if e.geo, err = ValidateProxyURL(spec.Geo); err != nil {
		return fmt.Errorf("egress chain geo: %w", err)
	}
	if strings.TrimSpace(spec.Sticky) != "" {
		if e.sticky, err = ValidateProxyURL(spec.Sticky); err != nil {
			return fmt.Errorf("egress chain sticky: %w", err)
		}
		e.stickySess = newStickySession()
		e.sticky.User = url.UserPassword(stickyUser(e.sticky.User.Username(), e.stickySess), stickyPassword(e.sticky.User))
	}
	if old := t.egress.Swap(e); old != nil && old.stop != nil {
		close(old.stop) // retire the previous chain's probe
	}
	e.probeLoop(t)
	return nil
}

// EgressChainJSON returns the persisted form of the active chain ("" when none).
func (t *Traffic) EgressChainJSON() string {
	e := t.egress.Load()
	if e == nil {
		return ""
	}
	spec := egressChainJSON{WS: e.ws.String(), Geo: e.geo.String()}
	if e.sticky != nil {
		// The session id is per-process state; persist the template without it and
		// re-derive on load, so a restart does not pin a dead session.
		u := *e.sticky
		spec.Sticky = u.String()
	}
	b, _ := json.Marshal(spec)
	return string(b)
}

// pickUpstream is the go-mitmproxy per-request upstream hook.
func (t *Traffic) pickUpstream(req *http.Request) *url.URL {
	e := t.egress.Load()
	if e == nil {
		return t.upstream.Load()
	}
	host := ""
	if req != nil && req.URL != nil {
		host = strings.ToLower(req.URL.Hostname())
	}
	if e.sticky != nil && e.stickied(host) {
		return e.sticky
	}
	if e.wsAvailable() {
		return e.ws
	}
	return e.geo
}

func (e *egressChain) wsAvailable() bool {
	until := e.parkedUntil.Load()
	return until == 0 || time.Now().Unix() >= until
}

// parkWS skips webshare for d. Logged once per park window.
func (e *egressChain) parkWS(why string, d time.Duration) {
	e.parkMu.Lock()
	defer e.parkMu.Unlock()
	now := time.Now()
	if until := e.parkedUntil.Load(); until != 0 && now.Unix() < until {
		return
	}
	e.parkedUntil.Store(now.Add(d).Unix())
	log.Printf("[egress] webshare parked for %s (%s); target traffic leaves through the geonode fallback", d, why)
}

func (e *egressChain) stickied(host string) bool {
	e.stickyMu.Lock()
	defer e.stickyMu.Unlock()
	until, ok := e.stickyHosts[host]
	if !ok {
		return false
	}
	if time.Now().Unix() >= until {
		delete(e.stickyHosts, host)
		return false
	}
	return true
}

func (e *egressChain) markSticky(host, why string) {
	if e.sticky == nil || host == "" || e.stickied(host) {
		return
	}
	e.stickyMu.Lock()
	e.stickyHosts[host] = time.Now().Add(e.stickyTTL).Unix()
	e.stickyMu.Unlock()
	log.Printf("[egress] %s answered %s; pinning it to the sticky session (%s) so cf_clearance stays valid", host, why, e.sticky.Host)
}

// noteEgress reacts to a completed exchange: park webshare when it is out of
// quota, switch a challenged host to the sticky session. The first 8 KiB of the
// body is enough — challenge pages are tiny.
func (t *Traffic) noteEgress(f *mproxy.Flow) {
	e := t.egress.Load()
	if e == nil || f == nil || f.Request == nil || f.Request.URL == nil || f.Response == nil {
		return
	}
	host := strings.ToLower(f.Request.URL.Hostname())
	status := f.Response.StatusCode
	// 402/407 come from the proxy itself (quota/auth), not the target. Only
	// attribute them to webshare while webshare is the one being used.
	if (status == http.StatusPaymentRequired || status == http.StatusProxyAuthRequired) && e.wsAvailable() {
		e.parkWS(fmt.Sprintf("HTTP %d", status), e.wsRetry)
		return
	}
	if status != 403 && status != 503 && status != 429 {
		return
	}
	if ctype := f.Response.Header.Get("Content-Type"); !strings.Contains(strings.ToLower(ctype), "html") {
		return
	}
	body := f.Response.Body
	if len(body) > 8<<10 {
		body = body[:8<<10]
	}
	if challengeRE.Match(body) {
		e.markSticky(host, fmt.Sprintf("HTTP %d challenge", status))
	}
}

// noteEgressError parks webshare when a request it was carrying fails at the
// transport level — an exhausted or rejecting provider usually answers with a
// reset, not a 407. The park is short (2 min) so one flaky target cannot pin the
// whole install to the fallback.
func (t *Traffic) noteEgressError(f *mproxy.Flow, err error) {
	e := t.egress.Load()
	if e == nil || err == nil || !e.wsAvailable() {
		return
	}
	if f != nil && f.Response != nil {
		return // a response came back; the quota path in noteEgress owns it
	}
	e.parkWS("transport error: "+err.Error(), 2*time.Minute)
}

// probeLoop watches webshare so an exhausted (or rejected) provider is parked
// before target traffic wastes a request on it. A provider that runs out of
// bandwidth usually resets the connection or answers 407, which go-mitmproxy
// surfaces before any flow exists — so the flow hooks in noteEgress cannot see it.
func (e *egressChain) probeLoop(t *Traffic) {
	if e.ws == nil {
		return
	}
	go func() {
		tick := time.NewTicker(15 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-t.closed:
				return
			case <-e.stop:
				return
			case <-tick.C:
				e.probeWS()
			}
		}
	}()
}

func (e *egressChain) probeWS() {
	tr := &http.Transport{Proxy: http.ProxyURL(e.ws), DisableKeepAlives: true, TLSHandshakeTimeout: 10 * time.Second}
	client := &http.Client{Transport: tr, Timeout: 20 * time.Second}
	resp, err := client.Get("http://api.ipify.org")
	if err != nil {
		e.parkWS("probe: "+err.Error(), e.wsRetry)
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 512))
	if resp.StatusCode >= 400 {
		e.parkWS(fmt.Sprintf("probe HTTP %d", resp.StatusCode), e.wsRetry)
		return
	}
	e.unpark()
}

func (e *egressChain) unpark() {
	if e.parkedUntil.Swap(0) != 0 {
		log.Printf("[egress] webshare is answering again; back to the primary egress")
	}
}

// newStickySession returns a fresh 22-char session id (allowed: [A-Za-z0-9_]{1,25}).
func newStickySession() string {
	b := make([]byte, 10)
	_, _ = rand.Read(b)
	return "jc" + hex.EncodeToString(b)
}

// stickyUser rewrites a geonode username into the sticky form:
//
//	<user>-country-<CC>-session-<id>-lifetime-<minutes>
//
// A username that already carries -session- is left alone (the operator pinned
// their own session).
func stickyUser(user, sid string) string {
	if strings.Contains(user, "-session-") {
		return user
	}
	return user + "-country-US-session-" + sid + "-lifetime-60"
}

func stickyPassword(u *url.Userinfo) string {
	if u == nil {
		return ""
	}
	p, _ := u.Password()
	return p
}
