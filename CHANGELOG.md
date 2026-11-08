# Changelog
## [Unreleased] - 2026-11-08

- Planner rounds now derive a task-specific threat model and mutate 13
  enforcement, identity, lifecycle, ordering, rollback, and composition angles
  into bounded, falsifiable worker intents.
- New facts, findings, and observed negative results feed target-specific
  sibling and chain variants back into the planner instead of restarting broad
  exploration. Workers verify state and side effects even after rejected or
  rolled-back operations.
- The mutation and execution policies are code-owned prompt tails, so upgrades
  apply them to existing installations without overwriting editable prompts.
- Headed signup support now uses a pinned, non-root Browserless sidecar instead
  of running Chrome beside JCyber secrets. Browser sessions have concurrency,
  resource, five-minute idle, and 30-minute hard limits; Tini and explicit
  client shutdown prevent zombie and stale processes.
- Browser-default upgrades now recognize runtime-added proxy arguments, so they
  cannot record completion while leaving the legacy headless launcher in place.
- Worker and pentest agents receive the Playwright CLI skill. Signup guidance
  keeps each test identity on one named session and sticky proxy through email
  verification, login, and credential creation.
- Source-code and native-binary investigations gain a tiered validation skill:
  static hypotheses must pass an isolated-function harness or focused fuzzer,
  then a minimal multi-component harness, before full builds or VMs are used.
- Planner and worker policies now keep static analysis as inferred evidence,
  gate each validation tier on executable reachability, and reserve confirmed
  findings for reproducible PoCs with controls.

## [Unreleased] - 2026-11-05

- `test_login_identity` ships with the app (seeded once, editable afterwards).
  It now returns a `sticky_proxy` next to every identity: a geonode sticky
  session whose egress IP is verified pinned across two probes. Signup, the
  verification click and the login all run from one IP, which is what keeps
  `cf_clearance` (IP+UA bound) alive instead of dying on a rotating exit.
- Egress fallback chain, in Go: webshare first, geonode when webshare is out of
  quota (402/407, or a reset caught by the 15 s probe), and a pinned sticky
  session for a host that answered a Cloudflare challenge. Configured by the
  `egress_chain` setting; per-request upstream selection, no sidecar process.
- `jc-proxy.sh auto|webshare|geonode|direct|status` flips the egress and rebuilds
  agents in place, so switching modes no longer needs the dashboard.
- `geo-relay.py` adds Proxy-Authorization for clients that cannot pass proxy
  credentials themselves (Chrome's `--proxy-server`), so a browser can reach a
  credentialed upstream.

## [Unreleased] - 2026-11-03

- Task thinking effort is selectable in task creation, the iMessage `create_task`
  tool, and `spawn_task`. `auto` uses each model's provider default; `max` keeps
  each selected profile's configured effort. Existing tasks keep their profile
  settings.
- iMessage bridge gains `rename_task` (via `PATCH /api/tasks/{id}`), and
  `create_task` now spells out that `name` is the display title and `description`
  is long-form context, so names stop landing in descriptions.
- Asset graph: legend and labels are in English, and colors follow the app theme
  instead of staying light inside dark mode.
- LLM Token Usage panel defaults to the New view; Legacy remains selectable.


## [Unreleased] - 2026-10-31

- Traffic capture now defaults **on**: the recording proxy, traffic tools and
  capture prompt reach agents out of the box.
- Automatic agent traffic binding now defaults **on**, so findings carry their
  request/response evidence without flipping a switch first.
- Login page: replaced the missing `/logo.svg` with `/logo.png`; the logo no
  longer renders as a broken image.
- Web client: 401s no longer reload `/login` and `/setup`, which had put the
  login page in an endless remount loop whenever a password was set.
- Build: fixed two strict-null type errors that failed `next build`.

## [1.0.0] - 2026-10-21

- Initial jcyber release.
- English UI, prompts, and seed data throughout.
- Attribution scrub: no upstream project names in binary or runtime traffic;
  unattributed user agents; module paths renamed; update source repointed.
- Operational stealth defaults for workers; dashboard binds to localhost.
