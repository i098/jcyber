#!/usr/bin/env bash
# jc-proxy — flip jcyber's global egress and rebuild its agents in place (no restart).
#
#   ./jc-proxy.sh status            # print the current egress
#   ./jc-proxy.sh auto             # chain: webshare -> geonode, sticky on a challenge (recommended)
#   ./jc-proxy.sh webshare         # pin every request to webshare (rotating per request)
#   ./jc-proxy.sh geonode          # geonode STICKY session — pinned IP, for logins / CF
#   ./jc-proxy.sh direct           # no proxy at all
#
# Why sticky exists: cf_clearance and risk scores are bound to IP+UA. A rotating
# egress invalidates them between signup, the verification click and the login, so
# any flow that must keep one identity uses `geonode` (or the per-identity
# sticky_proxy that the test_login_identity tool mints).
#
# Credentials come from the environment, falling back to the values already in
# this deployment. Override without editing the file:
#   JC_WEB_PROXY=http://user:pass@p.webshare.io:80 ./jc-proxy.sh webshare
#   JC_GEO='http://user-country-US-session-myid-lifetime-60:pass@proxy.geonode.io:10000' ./jc-proxy.sh geonode
set -euo pipefail
BASE=${JCYBER_URL:-http://127.0.0.1:8787}
KEY_FILE=${JCYBER_JWT_KEY:-./jwt.key}

WEB=${JC_WEB_PROXY:-'http://nsqgplth:eehs5c42ygri@p.webshare.io:80'}
# Sticky port 10000 + -session-<id>-lifetime-<minutes>. Session IDs match ^[A-Za-z0-9_]{1,25}$.
GEO=${JC_GEO:-"http://geonode_gMweMNW9Hr-country-US-session-jcdefault-lifetime-60:3f2b2585-d325-4ae6-b1ac-41f6d7330453@proxy.geonode.io:10000"}
# Rotating geonode (no session pin) — the fallback when webshare is out of quota.
GEO_ROT=${JC_GEO_ROT:-"http://geonode_gMweMNW9Hr:3f2b2585-d325-4ae6-b1ac-41f6d7330453@proxy.geonode.io:9000"}

CHAIN=$(CHAIN_WS=$WEB CHAIN_GEO=$GEO_ROT python3 -c 'import json,os;print(json.dumps({"ws":os.environ["CHAIN_WS"],"geo":os.environ["CHAIN_GEO"],"sticky":"http://geonode_gMweMNW9Hr-country-US-session-jcchain01-lifetime-60:3f2b2585-d325-4ae6-b1ac-41f6d7330453@proxy.geonode.io:10000"}))')

case "${1:-status}" in
  auto) P='' E=$CHAIN ;;
  webshare) P=$WEB E='' ;;
  geonode|login) P=$GEO E='' ;;
  direct) P='' E='' ;;
  status) P=__status__ E='' ;;
  *) echo "usage: $0 auto|webshare|geonode|direct|status" >&2; exit 2 ;;
esac

TOK=$(KEY_FILE=$KEY_FILE python3 - <<'PY'
import base64,hmac,hashlib,json,os,time
key=open(os.environ["KEY_FILE"]).read().strip().encode()
b=lambda x: base64.urlsafe_b64encode(x).rstrip(b'=')
h=b(json.dumps({"alg":"HS256","typ":"JWT"}).encode())
p=b(json.dumps({"sub":"jcyber","exp":int(time.time())+900,"iat":int(time.time())}).encode())
print((h+b'.'+p+b'.'+b(hmac.new(key,h+b'.'+p,hashlib.sha256).digest())).decode())
PY
)

if [ "$P" = __status__ ]; then
  curl -sS -m 10 -H "Authorization: Bearer $TOK" "$BASE/api/settings" \
    | python3 -c 'import sys,json;d=json.load(sys.stdin);print("chain:",d.get("egress_chain") or "(none)");print("global_proxy:",d.get("global_proxy") or "(direct)")'
else
  body=$(P=$P E=$E python3 -c 'import json,os;print(json.dumps({"global_proxy":os.environ["P"],"egress_chain":os.environ["E"]}))')
  code=$(curl -sS -o /dev/null -w '%{http_code}' -m 20 -X PUT -H "Authorization: Bearer $TOK" \
    -H 'Content-Type: application/json' -d "$body" "$BASE/api/settings")
  echo "set ${1} -> HTTP $code"
fi
