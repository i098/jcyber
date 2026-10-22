# api-recon — Reference Manual

Grep recipes, `config.json` templates, and troubleshooting. Run all grep commands against `js/`. For single-line bundles, optionally run `js-beautify` or `sed 's/}/}\n/g'` first; raw grep with a context window is usually sufficient.

## Script Notes

All files in `scripts/` are **reference templates** and must be adapted to the target site before execution. Typical changes:

| Script | Common Adjustments |
|---|---|
| `harvest_static.py` | Endpoint regex, webpack/Vite manifest parsing, microfrontend publicPath, retries/Concurrency |
| `runtime_harvest.js` | neutralize field names and success values, stub match rules and body structure, route sources, WS recording, `waitUntil`/`routeTimeout`/`proxy` |
| `preload.js` | `loginPathRe`, L1 stubs, `neutralize.fields`, `apiPattern`, whether to enable L3, `recordDetail`, `observe.*`, `neutralizeVueRouter` |
| `spider_mpa.py` | `--exclude` destructive links, cookie, depth/max, same-origin filtering |
| `extract_route_map.py` | `routeMap` / `routeLink` regex, KEY naming patterns |
| `build_perm_tree.py` | `userRouteAuth` parsing, `ROOTS`/`PREFIX_PARENT` hierarchy heuristics, outer stub field names |
| `config.json` | Central entry point for all site-specific parameters above |

Place adapted files in the Task working directory (such as `recon/`) and state specific changes from the reference scripts in the report.

---

## A. Reverse Engineer the Three Gates

### A1. Render Gate — "How Is Login Status Determined?"

```bash
grep -rhoaE '.{0,40}(isLogin|isAuthenticated|loggedIn|hasLogin|requireAuth)\b.{0,80}' js | head
grep -rhoaE 'function (getUser|getToken|getAuth)[0-9]?\([^)]*\)\{.{0,200}' js | head
grep -rhoaE '(localStorage|sessionStorage)\.getItem\("[^"]+"\)' js | sort -u
grep -rhoaE '(Cookies?|cookie)\.(get|load)\("[^"]+"\)' js | sort -u
grep -rhoaE '\batob\(|JSON\.parse\(|jwt|decode' js | head
```

Find the chain `isLogin = f(getUser())` → `getUser = decode(storage.read(KEY))` and identify the **storage key**, **container** (Cookie vs localStorage), and **encoding**:

| Encoding | Fabricated config Value |
|---|---|
| Plaintext string / `"1"` / token | `"value": "anything-truthy"` |
| `JSON.parse(x)` | `"value": "json:{\"id\":1,\"username\":\"admin\"}"` |
| `JSON.parse(atob(x))` | `"value": "b64json:{\"id\":1,\"username\":\"admin\"}"` |
| JWT | Unsigned/`alg:none` JWT, or sign with a key from the bundle |
| Encryption (SM2/AES/RSA) | Find hardcoded keys; forge when the render gate only needs a decodable blob; otherwise fall back to static analysis |

→ Write to `cookies` / `localStorage`.

### A2. Interceptor Gate — "What Triggers a Redirect to /login?"

```bash
grep -rhoaE '.{0,60}(interceptors\.response|axios|request\.use).{0,120}' js | head
grep -rhoaE '.{0,40}(response_code|errcode|errno|\bcode\b|\bret\b|\bstatus\b)\s*[=!]==?\s*[\-0-9]{1,4}.{0,60}' js | head -20
grep -rhoaE '.{0,40}(未登录|请重新登录|登录已过期|unauthorized|登录失效|授权|token.{0,10}invalid).{0,40}' js | head
grep -rhoaE '.{0,30}(location\.href|router\.(push|replace)|navigate)\([^)]*login[^)]*\)' js | head
```

Determine: **field name**, **success value** (usually `0` or `200`), and **failure values that trigger redirects**. Verify with a junk Session:

```bash
curl -sk -X POST -H 'Cookie: <fakekey>=junk' https://target/api/<protected> -d '{}' -H 'Content-Type: application/json'
```

→ Write to `neutralize.fields` + `neutralize.success`.

### A3. Content Gate — "Where Do Menus/Permissions Come From?"

```bash
grep -rhoaE '"/api[^"]*(permission|perm|role|menu|acl|resource|nav)[^"]*"' js | sort -u
grep -rhoaE '.{0,30}(menus|permissions|menuList|routeList|authList|role_permissions)\b.{0,120}' js | head
grep -rhoaE 'userRouteAuth|getResultTree|routeMap|routeLink|hasPermission|checkAuth' js | head
grep -rhoaE '([A-Z_][A-Z0-9_]*):\{name:"[^"]*",link:"/[^"]+"\}' js | head
```

**Two data layers** (common in enterprise admin panels):

| API | Typical Payload | Consumer |
|---|---|---|
| `.../role_permissions` | `{ permissions: string[], role_type }` | Route guards, button-level ACL |
| `.../permissions/all` | `tree[{ code, position, children }]` | Sidebar menu rendering |
| `userRouteAuth` in bundle | `{ CODE: { url, name? } }` | code → frontend path |
| `routeMap` in bundle | `{ KEY: { name, link } }` | Alias resolution (webpack `o.DASHBOARD`) |

Read consumer code to confirm how `getResultTree(tree, permissions)` filters and which field `v-if` / `hasAuth(code)` checks.

**Manual fabrication** (small sites): build a permissive payload → `stubs`.

**Full permission tree restoration** (large sites with blank sidebars/submodules): see **section I**.

---

## B. config.json Template

```json
{
  "baseUrl": "https://target/",
  "runtimeMode": "both",
  "chromium": "/usr/bin/chromium",

  "cookies": [
    { "name": "auth", "value": "b64json:{\"id\":1,\"username\":\"admin\",\"role\":\"admin\",\"func\":{},\"permissions\":[\"*\"]}" }
  ],
  "localStorage": { "token": "faketoken", "isLogin": "1" },

  "neutralize": {
    "fields": ["response_code", "code", "errno", "ret", "status"],
    "success": 0,
    "flags": { "success": true, "message": "ok" }
  },
  "forward": true,
  "loginUrlPattern": "/login",
  "apiPattern": "/api/|/rest/|/graphql",

  "mockTier": "L1+L2",
  "recordDetail": true,
  "observe": {
    "storageReads": false,
    "cookieReads": false,
    "xhrHeaders": true
  },
  "neutralizeVueRouter": true,
  "stubs": [
    {
      "match": "permissions/all|/menu|role_permissions",
      "body": {
        "response_code": 0, "code": 0,
        "data": {
          "permissions": ["*"],
          "menus": [
            { "name": "dashboard", "path": "/dashboard", "show": true, "children": [] },
            { "name": "alert", "path": "/alert", "show": true, "children": [] }
          ]
        }
      }
    }
  ],

  "explore": {
    "clickTabs": true,
    "clickTables": true,
    "pushStateFallback": true,
    "maxMenuItems": 50
  },

  "routes": ["/dashboard", "/alert", "/asset", "/device", "/report", "/config", "/system"],
  "waitMs": 1500, "perRouteMs": 900, "headless": true,
  "waitUntil": "domcontentloaded",
  "routeTimeout": 12000,
  "proxy": "",

  "captureResponses": true, "recordWs": true, "respMax": 600
}
```

Field descriptions:
- `runtimeMode`: `depth` (Puppeteer), `coverage` (browser MCP), `both`
- `cookies[].value` prefixes: `b64json:` → base64(JSON); `json:` → raw JSON; no prefix → literal
- `forward: true` forwards real requests and rewrites code fields; `false` uses fully offline stubs
- `mockTier`: preload layers enabled in coverage mode, such as `L1+L2`, `L1+L2+L3`
- `routes` comes from `routes.txt`; the harness automatically appends `<a href>` after menu fabrication
- `captureResponses` / `recordWs` only apply to depth mode
- `waitUntil`: use `domcontentloaded` for large SPAs to avoid `networkidle2` hanging
- `routeTimeout`: per-route `page.goto` timeout (milliseconds)
- `proxy`: Puppeteer `--proxy-server`; alternatively set `HTTP_PROXY` / `HTTPS_PROXY`

### B1. Dual-Stub Template (role_permissions + permissions/all)

```json
"stubs": [
  {
    "match": "role_permissions",
    "body": {
      "response_code": 0,
      "data": {
        "permissions": ["MONITOR", "MONITOR_ALERT", "THREAT", "ASSETS_RISK"],
        "role_type": "SUPER_ADMIN"
      }
    }
  },
  {
    "match": "permissions/all",
    "body": {
      "response_code": 0,
      "data": [
        {
          "code": "MONITOR",
          "position": 1,
          "children": [
            { "code": "MONITOR_ALERT", "position": 1, "children": [] }
          ]
        }
      ]
    }
  }
]
```

Outer field names (`response_code` / `code` / `data`) must match the A2 interceptor gate; `permissions` must cover every leaf code in the tree.

---

## C. coverage Mode: preload Configuration

Edit the top-level `CONFIG` object in `scripts/preload.js`, or replace it before CDP injection:

```javascript
const CONFIG = {
  loginPathRe: /\/(login|signin)(\/|$|\?)/i,
  mockTier: 'L1+L2',
  forward: true,
  recordDetail: true,
  extractUrlsFromResponse: true,
  neutralizeVueRouter: true,
  observe: { storageReads: false, cookieReads: false, xhrHeaders: true },
  neutralize: { fields: ['response_code', 'code'], success: 0 },
  stubs: [ /* Same as config.json stubs */ ],
  apiPattern: /\/(api|apis|v\d+|dev|internal|graphql)\//i,
};
```

Verify: `window.__API_RECON_PRELOAD__ === true` and the pathname stays stable.

Export recordings:

```javascript
JSON.stringify({
  apis: [...window.__API_RECON_LOG__],
  detail: window.__API_RECON_DETAIL__,
  routes: [...(window.__API_RECON_ROUTES__ || [])],
  observe: window.__API_RECON_OBSERVE__,
}, null, 2)
```

---

## D. preload / runtime Hook Capabilities

Built-in browser hooks and coverage in preload (coverage) and runtime_harvest (depth):

| Hook Capability | Value for API Discovery | Coverage |
|---|---|---|
| Hook fetch / XHR.open | Record request URLs/methods | ✅ `recordDetail` + `__API_RECON_LOG__` |
| Hook XHR.setRequestHeader | Discover headers such as Authorization | ✅ `observe.xhrHeaders` |
| Hook localStorage/cookie reads | Confirm Session key names | ⚠️ Optional `observe.storageReads/cookieReads` |
| Retrieve Vue routes | Complete frontendRoutes | ✅ `__API_RECON_ROUTES__` (loaded routes) |
| Neutralize Vue route guards / block login redirects | Expose modules to trigger APIs | ✅ `neutralizeVueRouter` + native redirect neutralization |
| Retrieve React routes | Complete routes | ⚠️ Static analysis + clicks; no dedicated hook |
| Block page redirects (login paths) | Stay on the page for analysis | ⚠️ Block only login paths to avoid disrupting business navigation |
| Hook encryption libraries (CryptoJS/SM etc.) | Encrypted parameters → plaintext API bodies | ❌ Manually hook encryption function inputs; record conclusions in config |
| Bypass anti-debugging | Otherwise runtime cannot record APIs | ❌ Handle manually; static analysis remains available |

---

## E. Endpoint Extraction Regex (When Static Results Are Sparse)

Relax `extract_endpoints` in `harvest_static.py`, or run manually:

```bash
grep -rhoaE '"/[a-z][A-Za-z0-9_/\-]{3,}"' js | sort -u
grep -rhoaE '/api/[a-zA-Z0-9_./-]+' js | sort -u
```

---

## F. Troubleshooting

| Symptom | Cause → Action |
|---|---|
| Few static APIs | Endpoint dialect mismatch → relax regex (section D) |
| Chunk count ≪ manifest | CSS-only or undeployed chunks; 404s already retried |
| Runtime still shows login page | Incorrect render gate → recheck A1: key, container, encoding, domain |
| Shell opens but modules are blank | Content gate → fabricate menus (A3); `routes` paths may be wrong |
| Only bootstrap/locale on every route | Incomplete permission codes → section I permission tree restoration; check dual `role_permissions` + `permissions/all` stubs |
| Sidebar items exist but child pages are blank | Missing intermediate tree nodes or codes inconsistent with `userRouteAuth` |
| Every API redirects to login | Interceptor gate → confirm `neutralize`; nested fields require extending the walk logic |
| Zero WS frames | Subscription requires user interaction; increase `perRouteMs` |
| Empty response bodies | Real responses are available only with `forward: true` |
| Chromium missing | Install chromium or set `config.chromium` / `CHROMIUM` |
| Still redirects to login despite many mocks | Hook injected too late or missing `location.href` setter → document-start + preload |
| All lists empty | L3 empty arrays are normal; keep clicking tabs/settings/details |
| Redux actions mistaken for routes | Filter internal paths containing get/set/change/clear/toggle/upload |
| Vue still redirects to login | preload not at document-start → change injection timing; or clear guards manually when `neutralizeVueRouter: false` |
| URLs in responses missing from logs | Enable `extractUrlsFromResponse`; or extract manually from `__API_RECON_DETAIL__` |
| Unknown Authorization header name | Enable `observe.xhrHeaders` or inspect request headers in DevTools |
| Runtime very slow / Timeout | Set `waitUntil: domcontentloaded`; reduce `routeTimeout`; avoid `networkidle2` |
| Proxy connection failure | Check `proxy` / environment variables; Puppeteer and curl must use the same proxy port |

---

## G. Hardened Targets

When the server validates Sessions at each step (unforgeable signed cookies, server-rendered menus that cannot be stubbed), runtime analysis stalls at the shell. Expected behavior:

- **Static analysis is sufficient for Endpoint enumeration** — module paths are in the code
- If authorization permits, run the same harness with a **real Session**: `forward: true`, no neutralize needed; capture real methods/params/responses

---

## H. Per-Task Checklist

1. Confirm the authorized scope
2. **Read** `scripts/harvest_static.py` → adapt to the target → run → review `api_static.txt`, `routes.txt`
3. **Phase 1b**: path anchor-window expansion + binding layer → `param_candidates.json` (section J)
4. Reverse engineer A1/A2/A3 → write site-specific `config.json`
5. **Read and adapt** `runtime_harvest.js` / `preload.js` before execution
6. `runtimeMode=depth`: `npm install` → run the adapted harvest script
7. `runtimeMode=coverage/both`: inject adapted preload at document-start → browser MCP dynamic enumeration + **parameter trigger matrix**
8. Modules not rendering → **section I permission tree restoration** → patch stubs → rerun
9. Parameter multi-sample diffs + inference from errors → `params_merged.json`
10. Merge → `site_map.json` + `api_merged.txt`; honestly state coverage, gaps, and script changes

---

## I. Permission Tree Restoration (Phase 4 Deep Dive)

Use when fabricating a simple `menus: [{ path, show: true }]` does not work and submodules still do not mount.

### I1. Locate the auth Module

```bash
grep -l 'userRouteAuth' js/*.js
grep -l 'routeMap\|routeLink' js/*.js
grep -rhoaE 'getResultTree|role_permissions|permissions/all' js | head
```

Record: **permission API paths**, **response field names**, **consumer chunk filenames**.

### I2. Extract routeMap

```bash
python3 scripts/extract_route_map.py recon/js recon/
# Produces recon/route_map.json
```

If `[!] no routeMap pattern found`: relax the regex in `extract_route_map.py`, or grep manually:

```bash
grep -rhoaE '([A-Z_][A-Z0-9_]*):\{name:"[^"]*",link:"/[^"]+"\}' js | head -20
```

### I3. Build the Permission Tree + Stubs

```bash
python3 scripts/build_perm_tree.py recon/js recon/ --config recon/config.json
```

Script logic:
1. Parse `userRouteAuth={MONITOR:{url:...},...}` (including webpack aliases such as `He=o.DASHBOARD`)
2. Resolve aliases to real paths using `route_map.json`
3. Infer parents from code prefixes (`MONITOR_ALERT` → `MONITOR`)
4. Output `permissions_tree.json`, `permissions_all_stub.json`, `role_permissions_stub.json`
5. With `--config`, automatically write `stubs` and extend `routes` in `config.json`

**Adapt to the target** (at the top of the script):
- `DEFAULT_ROOTS`: list of top-level module codes
- `DEFAULT_PREFIX_PARENT`: `PREFIX_` → parent mappings
- `DEFAULT_EXTRA_PARENT`: orphan nodes whose relationships are not prefix-based

### I4. Verify Stub Consistency

```bash
# permissions count should be approximately the number of userRouteAuth entries
wc -l recon/perm_codes_all.txt
# routes should cover every link in route_map
python3 -c "import json; m=json.load(open('recon/route_map.json')); r=set(json.load(open('recon/config.json'))['routes']); print('missing', [v['link'] for v in m.values() if v['link'] not in r])"
```

### I5. Rerun Runtime Analysis and Compare

```bash
node recon/runtime_harvest.js recon/config.json
# Compare runtime_api.json counts before/after fabrication; check for module APIs under /attack, /asset, etc.
```

| Before Fabrication | After Fabrication (Success) |
|---|---|
| Same 3–5 bootstrap calls per route | Different routes trigger different module APIs |
| Only `/api/locale/language` | Module endpoints appear under `/api/web/...` |
| Single-digit route count in `routes.txt` | `routes` contains 80–110+ entries from route_map |

### I6. If It Still Fails

- **coverage mode**: click sidebar items + tabs; permission-gated requests may occur only after interaction
- **Stub fields**: compare real API nesting (curl + real Session) with stub nesting
- **Additional guards**: grep button-level checks such as `hasPermission|checkRole|func.` and extend `role_permissions.permissions`
- **Static fallback**: module API paths remain in `api_static.txt`; runtime only supplements METHOD/body; retain `param_candidates.json` + recorded samples

---

## J. Parameter Reverse Engineering (Phase 1b / 5b / 5c)

**A methodology, not a universal script.** Find paths with regex; find parameters with anchor-window expansion + UI binding chains + multi-sample diffs + inference from errors.

### J1. Anchor-Window Expansion — Find Payload Objects from Paths

```bash
# Use known paths from Phase 1 as anchors
grep -n '"/api/user/list"' js/*.js
grep -rhoaE '.{0,120}("/api[^"]+").{0,200}' js | head
grep -rhoaE '(params|data|body|payload)\s*:\s*\{' js | head
grep -rhoaE '(get|post|put|delete|patch)\([^,]+,\s*\{' js | head
```

### J2. Wrapper Layers and Transport Forms

```bash
# axios / shared request wrapper
grep -rhoaE '(axios|request)\.(get|post|put|delete|patch)\(' js | head
grep -rhoaE 'interceptors\.(request|response)' js | head

# GraphQL
grep -rhoaE '(query|mutation)\s+\w+|gql`|graphql\(' js | head
grep -rhoaE '\$[a-zA-Z_]+\s*:\s*(Int|String|Boolean|\[)' js | head

# FormData / multipart
grep -rhoaE 'FormData|\.append\(' js | head

# Path parameters
grep -rhoaE 'path:\s*"/[^"]*:[^"]+"' js | head
grep -rhoaE 'useParams|route\.params|\$route\.params' js | head
```

### J3. Validation Gate — Required Fields / Formats / Enums

```bash
grep -rhoaE '(required|message|pattern|enum|validator)\s*:' js | head
grep -rhoaE 'yup\.|zod\.|async-validator|Form\.Item|a-form-item|el-form-item' js | head
grep -rhoaE 'rules\s*:\s*\[|name:\s*["\'][a-zA-Z_]+["\']' js | head
grep -rhoaE 'label.*value|options\s*:\s*\[' js | head
```

### J4. Binding Layer — Forms → API

```bash
grep -rhoaE 'onFinish|handleSubmit|getFieldsValue|validateFields' js | head
grep -rhoaE '(pick|omit|transform|dayjs|moment)\(' js | head
```

Runtime supplement: DevTools → Network → request → **Initiator** (call stack); trace upward from `fetch`/`send` to payload assembly functions.

### J5. Encrypted Parameters

```bash
grep -rhoaE 'encrypt|decrypt|sign|CryptoJS|sm2|sm3|sm4|RSA|AES' js | head
```

**Do not guess fields from ciphertext** — hook encryption function **inputs** and record plaintext payloads before encryption; write conclusions to `config.json` / `param_candidates.json`.

### J6. Parameter Trigger Matrix (Required in Phase 3)

Record each operation once per module and diff request bodies/queries:

| Operation | Focus |
|---|---|
| Initial list load | Pagination defaults |
| Search | keyword, filters |
| Advanced filters | Optional fields |
| Create/edit | Full entity |
| Bulk/export | `ids[]`, `exportType` |
| Sort/paginate | `sortField`, `order` |

Produce `param_samples.json`: `[{ "path", "method", "action": "search", "body", "query", "headers" }]`

### J7. Confidence Rules

| Confidence | Condition |
|---|---|
| **High** | Static callsite + ≥2 consistent runtime samples |
| **Medium** | Static only, or only 1 runtime sample |
| **Low** | Inferred from responses/errors, not verified a second time |
| **Pending trigger** | Fields known statically but not reached through UI/permissions |

### J8. Quick Setup by Scenario

| Scenario | Sequence |
|---|---|
| REST list page | J1 payload object → J6 four-run diff → J3 rules |
| Create/edit form | J3 Form name → J4 submit chain → runtime submit + intentionally leave fields empty to inspect 400 |
| GraphQL | J2 variables declarations → record runtime variables for each operation |
| Encrypted body | J5 hook inputs → pre-encryption fields are the real params |

### J9. Mapping to api-recon Phases

| api-recon | Parameter Recon |
|---|---|
| Phase 1 Static | J1 anchor-window expansion |
| Phase 2 A2 Interceptor | Globally injected fields (tenantId, sign) |
| Phase 3 Runtime | J6 trigger matrix + `param_samples.json` |
| Phase 4 Permission Tree | Different modules have different forms → sufficient permissions are required to trigger all fields |
| Phase 5 Merge | `params_merged.json` + confidence; do not determine required fields from a single sample |

### J10. Troubleshooting

| Symptom | Action |
|---|---|
| Field names found statically never appear at runtime | Mark "pending trigger"; complete the permission tree / click advanced filters / try each dependent select option |
| Same path, different body shapes | Normal — record separate entries by `action`; do not force schemas together |
| Stub responses are fake but params are needed | **Inspect outbound request** bodies/headers; do not infer from stub responses |
| 400 reports a nested field | Watch for outer `data`/`bizData`/`variables` wrappers |
| GraphQL shows only operation names | Expand `variables` JSON; statically search for `$var: Type` |

---
