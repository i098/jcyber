---
name: api-recon
description: Invoke this skill when collecting a website's API endpoints.
---

# API Recon (Frontend Endpoint Reconnaissance)

With **authorization**, discover as completely as possible: **backend APIs** (paths, methods, parameters, response bodies), **frontend routes**, and **UI feature triggers** (tabs, dialogs, table actions, etc.).

---

## Boundaries and Prohibitions (Required Reading for Agents; Violations Are Out of Scope)

This skill **only performs API / parameter-surface reconnaissance**, not vulnerability discovery or exploitation.

### Task Boundaries

| Scope | Allowed | Prohibited |
|---|---|---|
| **Targets** | Enumerate paths, methods, parameters, routes, and UI triggers | SQLi/XSS/authorization bypass/brute-force/fuzzing for vulnerabilities, request-tampering attacks, destructive operations |
| **Authentication** | Hooks + stubs/mocks to bypass **client-side** login gates | Asking users for or guessing Credentials; attempting real login form submissions |
| **Runtime** | Hook endpoints without Credentials and use mock responses to enter the SPA's post-login shell | Flows that require a real backend Session to proceed |

### Dynamic Analysis Without Credentials (Default for Phase 3)

1. Use `preload.js` / `runtime_harvest.js` to **intercept and stub** bootstrap endpoints for login, permissions, menus, etc.;
2. Return mock bodies with **correct structure, successful business codes, and optionally empty data** for business query endpoints;
3. Allow the frontend to render post-login pages even without a backend or when receiving 401 responses, triggering more XHR/fetch/WebSocket requests;
4. **Empty data, empty tables, and placeholder UI are expected**. Do not switch to real login or vulnerability testing because of them.

**In short**: use mocks to expose frontend routes and mount components, and **record only outbound requests**. Backend responses are not the focus; what matters is **which additional endpoints the frontend calls**.

### Strict Workflow Prohibitions

| Prohibited | Alternative |
|---|---|
| Using grep/curl/Read on the main `index-*.js` entry to extract API paths before Phase 1 finishes | Run `OUTDIR/harvest_static.py` |
| Writing `extract_apis.py` or other scripts to replace harvest | Modify `OUTDIR/harvest_static.py` and rerun |
| Repeating the same grep/command after ≥2 failures | Change strategy: read tool_logs, modify harvest, consult the reference |
| Skipping gates A/B and running the original `scripts/` versions directly | Copy them to OUTDIR and adapt them to the target |
| Real usernames/passwords, OTP, OAuth, or other authentication | Use stubs/mocks (see above) |
| Skipping stubs to perform authorization bypass/injection tests in order to "get real data" | Record only outbound requests; stay within recon boundaries |
| Irreversible operations such as deletion, sensitive-data export, or bulk writes | The same restriction applies to coverage clicks |
| Claiming all pages and endpoints are known without completing runtime analysis + dynamic enumeration | See "Definition of Done" or document limitations |
| Claiming all parameters are known without completing the parameter trigger matrix + diff | Phase 3b matrix + Phase 5 diff |
| Inferring required/optional fields from a single runtime sample | Compare multiple samples or infer from validation rules/errors |

---

## Two-Layer Model + Runtime Modes

| Layer | Output | Limitations |
|---|---|---|
| **Static** (JS bundle) | Full Endpoint paths, draft routes, candidate fields at payload assembly sites | No HTTP methods; parameters require Phase 1b; misses URLs assembled at runtime |
| **Runtime** (live Session) | Methods + bodies + responses + dynamic URLs + WS/SSE; multi-sample diffs complete parameters | Pages must actually render to issue requests; one sample cannot establish required/optional fields |

| Runtime Mode | Engine | Use Case |
|---|---|---|
| **depth** | `runtime_harvest.js` (Puppeteer) | API inventory, METHOD/params/response bodies, WS/SSE, reproducible batch runs |
| **coverage** | browser + `preload.js` | Click tabs/dialogs/tables for deeper feature coverage |
| **both** | depth first, then coverage | Most complete; takes longest |

**Parameter methodology** (no universal script): use harvest/regex for paths; use **anchor-window expansion + UI binding chains + multi-sample diffs + inference from errors** for parameters (grep recipes in [reference.md](reference.md), section J).

---

## Definition of Done

Claim recon is complete only when all conditions are met:

- [ ] **Static**: Phase 1 harvest produces `api_static.txt`, `routes.txt`, and `js/`
- [ ] **Runtime**: at least depth or coverage; coverage/both requires **working hooks + a dynamic enumeration loop**
- [ ] **Shell access**: visiting business paths does not leave the app at `/login` (watch for hash routing)
- [ ] **Parameters**: coverage/both completes the parameter trigger matrix + `param_samples.json`; Phase 5 merges `params_merged.json`
- [ ] **Depth** (if module pages are blank): restore the permission tree in Phase 4 and rerun until **module-level APIs** appear (not only locale/bootstrap)
- [ ] **Delivery**: all Phase 5 outputs exist (see its output table); `insert_assets` writes service and Endpoint Assets

---

## Scripts and Gates

`scripts/` contains reference templates only. **Do not** run unmodified versions and treat their output as final.

**Rule**: read first → adapt to the target → write to `OUTDIR` (such as `recon/`) → record `CHANGES.md`; if unsuitable, rewrite using the methodology and reuse only the structure.

| Gate | When | Reference Script → OUTDIR Copy | Common Required Changes |
|---|---|---|---|
| **A (Static)** | After Phase 0, before the **first** harvest/spider run | `harvest_static.py` / `spider_mpa.py` | **Default regex works for most sites**; only adjust Endpoint regex, webpack/Vite `publicPath`, or MPA exclude/cookie when the manifest/dialect does not match |
| **B (Runtime)** | After Phase 2, before depth/coverage | `runtime_harvest.js` / `preload.js` + `config.json` | Cookie/localStorage keys, neutralize success values, stubs, login regex, API prefixes, hash/history |

**Mandatory SPA order** (not interchangeable; phase numbering takes precedence over "explore first, script later"):

| Step | Required | Prohibited |
|---|---|---|
| After Phase 0 | Next Bash command = `python3 OUTDIR/harvest_static.py <URL> OUTDIR` | curl/grep/Read on the main `index-*.js` entry (usually >500KB) |
| Gate A | Copy script → make small changes if needed → **run immediately** | Manually extract APIs before deciding whether to harvest |
| Before Phase 1 finishes | Check output with `wc -l`; modify harvest and retry on 404 | Write extraction scripts; repeatedly grep URLs that were not downloaded |
| From Phase 1b onward | grep only `OUTDIR/js/*.js` | Use the main bundle instead of harvest |

- ✅ Copy `harvest_static.py` → (optionally) adjust regex → **run immediately**
- ❌ curl the main bundle → grep repeatedly → write a temporary extractor → harvest only at the end
- **MPA**: after Phase 0, next Bash command = `python3 OUTDIR/spider_mpa.py ...`

---

## Tool and Output Constraints

| Constraint | Description |
|---|---|
| Large files | **Do not** Read/grep `index-*.js` files >100KB into context; batch-process them with OUTDIR scripts |
| grep output | Must use `\| head -20` or `-m 5`; keep only path summaries in Chat, not bundle excerpts |
| Verification | Use `wc -l`, `ls \| wc -l`; do not Read entire directories |
| Initial regex exploration | Optional, ≤1 attempt, only chunks ≤50KB or HTML; harvest is authoritative for formal static analysis |
| reference | Recipes/templates/troubleshooting are in [reference.md](reference.md); do not repeat the full text inline |

---

## Execution Roadmap

```
Phase 0 Classification + OUTDIR
  → Gate A → Phase 1 harvest (★ run immediately ★)
  → Phase 1b Parameter reverse engineering
  → Phase 2 Three authentication gates → config.json
  → Gate B → Phase 3 Runtime + parameter matrix
  → Phase 4 Permission tree (if needed) → rerun Phase 3
  → Phase 5 Merged report + insert_assets: bulk-insert every discovered service and Endpoint API Asset; never omit discovered Assets
```

Check off in order; **do not enter the next phase before completing the previous item**.

1. [ ] **Phase 0**: identify SPA/MPA; create `OUTDIR` → [Phase 0](#phase-0--classification)
2. [ ] **Gate A + Phase 1**: copy scripts → harvest **immediately** → verify with `wc -l` → [Phase 1](#phase-1--static)
3. [ ] **Phase 1b**: anchor-window expansion + binding layer → `param_candidates.json` → [Phase 1b](#phase-1b--parameter-reverse-engineering)
4. [ ] **Phase 2**: three authentication gates → `config.json` → [Phase 2](#phase-2--three-authentication-gates)
5. [ ] **Gate B**: adapt runtime scripts → [Phase 3](#phase-3--runtime)
6. [ ] **Phase 3**: depth / coverage / both; confirm shell access; parameter trigger matrix → `param_samples.json`
7. [ ] **Phase 4** (if needed): permission tree → patch stubs → rerun Phase 3 → [Phase 4](#phase-4--permission-tree-restoration)
8. [ ] **Phase 5**: merge outputs + report + `insert_assets` → [Phase 5](#phase-5--merge-and-report)

---

## Phase 0 — Classification

Fetch entry HTML and **create `OUTDIR`** (do not modify `scripts/` inside the skill):

- **SPA**: empty shell + `<div id=app>` + chunks → Phase 1–5
- **MPA**: SSR + `<form>`, no Endpoint bundle → after gate A:

```bash
python3 recon/spider_mpa.py <BASE_URL> <OUTDIR> [--cookie "session=..."] [--max 300] [--depth 5] [--exclude "logout|delete|destroy"]
```

Produces `forms.txt`, `links.txt`, and `api_inline.txt`. If SPA forms ≈ 0 → switch to Phase 1.

---

## Phase 1 — Static

Follow [Scripts and Gates](#scripts-and-gates) · [Tool and Output Constraints](#tool-and-output-constraints).

```bash
python3 recon/harvest_static.py <BASE_URL> <OUTDIR>
```

harvest: parse HTML scripts → webpack/Vite manifest → download all lazy chunks → produce `js/`, `api_static.txt`, `routes.txt`, and `chunkmap.txt`.

```bash
wc -l OUTDIR/api_static.txt OUTDIR/routes.txt
ls OUTDIR/js | wc -l
```

- Chunk count vs manifest: on 404, modify harvest and retry; do not curl chunks individually
- Too few entries in `api_static.txt` → relax the Endpoint regex in OUTDIR and rerun (see reference)

### Phase 1b — Parameter Reverse Engineering

Paths come from Phase 1; parameter fields require separate recon. See [Tool and Output Constraints](#tool-and-output-constraints) for grep rules.

**Completion criterion**: for important endpoints, identify field names, transport locations, inferred types, required status, sample values, and confidence.

#### 1b.0 — Transport Forms

| Form | Parameter Location | Static Analysis Priority |
|---|---|---|
| REST JSON | body + query | `(params\|data\|body)\s*:\s*\{` next to path anchors |
| GraphQL | `variables` | gql templates, `$page: Int` |
| Traditional forms | urlencoded | `<form>`, `FormData` |
| File uploads | multipart | `FormData.append` |
| Path parameters | `/user/:id` | Route tables + `useParams` / `$route.params` |
| Encryption/signatures | Wrapped in `sign`/`data` | Hook encryption function inputs (reference section D) |

Output: annotate each Endpoint with `transport: query|json|form|graphql|encrypted`.

#### 1b.1 — Anchor-Window Expansion

Use a known path as an anchor and expand its window to find payload assembly objects:

```bash
grep -n '"/api/user/list"' OUTDIR/js/*.js | head -20
grep -rhoaE '.{0,120}("/api[^"]+").{0,200}' OUTDIR/js/*.js | head -20
grep -rhoaE '(params|data|body|payload)\s*:\s*\{' OUTDIR/js/*.js | head -20
```

| Wrapper Layer | Parameter Clues |
|---|---|
| axios instance | `data` / `params` |
| Shared request wrapper | Interceptors inject global fields |
| OpenAPI client | Generated method signatures |
| React Query / SWR | Second hook argument |
| Vue composable | Composable inputs |

Type traces: `yup`/`zod`/rules, `Form.Item name=`, embedded Swagger.

→ `param_candidates.json`: `{ path, fields[], source: "static-callsite", confidence }`

#### 1b.2 — Binding Layer

```
Form field → onFinish/handleSubmit → transform → API payload
```

| Binding Source | Technique |
|---|---|
| Form submit | Follow submit → transform → API |
| Table search | `getFieldsValue()` → `params` |
| Route | `:id` / `?tab=` |
| Interceptor | Global `tenantId`, pagination, sign |
| Enum select | `options` → API enum values |

Trace upward from `fetch`/`XHR.send` in the DevTools call stack to payload assembly functions.

#### 1b.3 — Three Payload Questions (≠ Phase 2 Authentication Gates)

| Question | Required Answer |
|---|---|
| **Assembly** | Where the payload is built; traces of transforms |
| **Validation** | required, pattern, enum |
| **Transport** | path / query / body / multipart / headers |

At the interceptor gate (Phase 2), also inspect globally injected fields (Authorization, `X-Tenant-Id`, sign).

#### 1b.4 — Handoff to Phase 3

Candidate fields come from static analysis/the binding layer; **required/optional/conditional dependencies** require the Phase 3 parameter matrix + diff + Phase 5 inference from errors.

---

## Phase 2 — Three Authentication Gates

grep in `OUTDIR/js/` (with `head`) and write `config.json` (recipes in reference):

| Gate | Question | Keywords |
|---|---|---|
| **Render gate** | How is login status determined? | `isLogin`, `getToken`, Cookie/localStorage |
| **Interceptor gate** | What triggers redirects to `/login`? | `response_code`, `errno`, axios interceptor |
| **Content gate** | Where do menus/permissions come from? | `menu`, `permission`, `role`, `acl`, `routes` |

Do not treat localStorage key names as Credentials; confirm through chunks/request chains.

**Exit = Gate B**: record conclusions in `config.json` and modify `OUTDIR/runtime_harvest.js` / `preload.js`.

### Phase 2b — API Observation (Optional)

Use `preload.js` in OUTDIR to confirm Session key names, Authorization, and nested API URLs:

| Configuration | Output |
|---|---|
| `recordDetail: true` | `__API_RECON_DETAIL__` |
| `observe.xhrHeaders: true` | Header observations |
| `extractUrlsFromResponse: true` | Child APIs in responses |
| `observe.storageReads/cookieReads: true` | Fill in config |
| `neutralizeVueRouter: true` | `__API_RECON_ROUTES__` |

Export after each coverage round: `__API_RECON_LOG__`, `__API_RECON_DETAIL__`, `__API_RECON_ROUTES__`, `__API_RECON_OBSERVE__`.

---

## Phase 3 — Runtime

Gate B must be complete; follow [Boundaries and Prohibitions](#boundaries-and-prohibitions-required-reading-for-agents-violations-are-out-of-scope) and the mock strategy without Credentials.

Set `"runtimeMode": "depth" | "coverage" | "both"` in `config.json` (template in reference).

### Hooks and Stubs (Shared by depth + coverage)

| Layer | Scope | Purpose |
|---|---|---|
| L1 Exact | auth/permission/bootstrap stubs | Pass initial authentication |
| L2 Negative-code correction | All JSON responses | Unauthenticated codes → success |
| L3 Fallback | `/api` etc. not matched by L1 | Empty success bodies to expose the UI |

- **depth**: fake auth + `forward` business-code rewriting + `stubs`; traverse `routes` (hash/history); produce `runtime_api.json`
- **coverage**: inject `preload.js` at **document-start** (CDP `addScriptToEvaluateOnNewDocument` or Userscript)

Verify: `window.__API_RECON_PRELOAD__` exists; business paths do not redirect to `/login`.

```bash
cd recon && npm install
node runtime_harvest.js config.json
```

### 3b — Dynamic Enumeration for coverage (Required)

1. Main navigation/sidebar — click every item; wait 1–3s for network activity
2. Tabs — `role=tab`, `.ant-tabs-tab`
3. Tables — first-row view/edit/details
4. Toolbars — export, filter, create (**avoid irreversible deletion**)
5. On entering each module — merge APIs/routes
6. SPA — controlled `pushState` for uncovered paths in `routes.txt` (prohibited for MPA)

**Parameter trigger matrix** (required): record each operation type once per module and **diff multiple samples**:

| Operation | Typical Additional Parameters |
|---|---|
| Initial list load | Pagination + default filters |
| Search click | keyword, filter |
| Advanced filters | More optional fields |
| Create/edit | Full entity |
| Bulk/export/sort | `ids[]`, `exportType`, `sortField` |

**Outbound bodies/headers remain real under stubs**; use requests as the source of truth. Record → `scan_raw.json`, `param_samples.json`, `api_detail.json`.

- **Vue**: `neutralizeVueRouter: true` + document-start preload
- **React**: `routes.txt` + sidebar clicks + `pushState`
- **both**: 3a depth first, then 3b coverage

---

## Phase 4 — Permission Tree Restoration

**Trigger**: blank module pages / only bootstrap (such as locale) per route → content gate not passed.

| Symptom | Meaning |
|---|---|
| Shell access succeeds | Render gate + interceptor gate passed |
| Missing sidebar items/blank pages on click | Incomplete stub shape or permission codes |
| Same, very few APIs on every route | `v-if permission` not satisfied |
| Far fewer routes in `routes.txt` than the bundle | Complete them from the auth module |

```bash
grep -rhoaE '"/api[^"]*(permission|perm|role|menu|acl)[^"]*"' OUTDIR/js/*.js | sort -u | head -30
grep -rhoaE 'userRouteAuth|getResultTree|routeMap|routeLink|menuList|authList' OUTDIR/js/*.js | head -20
```

Typical chain: `role_permissions` (flat codes) + `permissions/all` (tree) → `getResultTree` → `userRouteAuth[CODE].url`.

```bash
python3 recon/extract_route_map.py recon/js recon/
python3 recon/build_perm_tree.py recon/js recon/ --config recon/config.json
```

Intermediate outputs: `route_map.json`, `userRouteAuth.json`, `permissions_tree.json`, `*_stub.json`, `perm_codes_all.txt`.

Stub checks: outer `response_code` agrees with the interceptor gate; flat codes align with the tree; `routes` covers every link in `route_map`.

After updating `config.json`, **rerun Phase 3**. For large SPAs, adjust `waitUntil`, `routeTimeout`, and `perRouteMs` (reference sections A3/I).

---

## Phase 5 — Merge and Report

### Output Table

| File | Phase | Content |
|---|---|---|
| `js/`, `api_static.txt`, `routes.txt`, `chunkmap.txt` | 1 | Static bundles and paths |
| `param_candidates.json` | 1b | Static candidate parameter fields |
| `config.json` | 2 | Three gates + runtime configuration |
| `runtime_api.json` | 3a | Detailed depth recordings (including WS/SSE) |
| `param_samples.json`, `scan_raw.json`, `api_detail.json` | 3b | Multiple samples, click logs, details |
| `route_map.json` etc. | 4 | Permission tree intermediate files (if performed) |
| `params_merged.json` | 5 | Merged parameter fields + confidence |
| `api_merged.txt` | 5 | `METHOD /path [params] [static\|runtime\|both]` |
| `site_map.json` | 5 | Routes, APIs, params, features, limitations |
| **insert_assets** | 5 | Write all service and Endpoint Assets to the Asset store |

### 5b — Parameter Merge

Diff `param_samples.json`; **there is no universal merge script**. Confidence rules are in reference J7 (high/medium/low/pending trigger).

### 5c — Inference from Errors

Within the authorized scope, send incomplete requests to inspect 400 errors (**parameter recon, not vulnerability testing**): `field 'x' is required`, enum errors, etc. Watch for `data` wrappers, `variables`, and pre-encryption `bizData`.

The report must state runtimeMode, static/runtime API counts, parameter confidence, uncovered modules, and a `CHANGES.md` summary of differences from the reference scripts.

Suggested `site_map.json` structure:

```json
{
  "site": "https://example.com",
  "runtimeMode": "both",
  "appType": "vue-spa",
  "routeGuardStrategy": ["nav-neutralize", "L1-auth", "L2-patch", "forward"],
  "apisFromStatic": [],
  "apisFromRuntime": [],
  "apis": [],
  "params": [{ "method": "POST", "path": "/api/user/list", "transport": "json", "fields": [] }],
  "frontendRoutes": [],
  "routesVerifiedByClick": [],
  "featuresTriggered": [],
  "limitations": ""
}
```

For more fields and grep recipes, see [reference.md](reference.md).

---

## General Notes

- **Framework-independent**: the same approach works for webpack/Vite/Angular lazy loading
- **Transport**: REST/JSON, GraphQL, WebSocket, SSE; gRPC-web is out of scope
- **SSR**: client fetch can be recorded; RSC/Server Actions cannot be fully enumerated
- **Blind spots**: JSVMP, WASM, strict HMAC/mTLS validation → static analysis + documented limitations
- **Parameter blind spots**: conditional dependencies, hidden params, WASM payload assembly → "pending trigger"/"unreachable"
- **Static analysis is the safety net**: it can still enumerate endpoints when runtime access is blocked

---

## Additional Resources

- Grep recipes, `config.json` templates, troubleshooting, hooks, parameter reverse engineering in section J, and site_map templates: **[reference.md](reference.md)**
- Reference script paths: see the [Scripts and Gates](#scripts-and-gates) table
