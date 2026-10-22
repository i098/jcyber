<p align="center">
  <img src="web/public/logo.png" height="90" alt="jcyber" />
</p>

<h1 align="center">jcyber</h1>

<p align="center">
  <strong>An autonomous penetration testing console — point it at an authorized target,<br/>
  it decomposes the goal, explores, verifies, and writes the report.</strong>
</p>

<p align="center">
  <a href="https://golang.org/doc/devel/release.html"><img src="https://img.shields.io/badge/go-1.26-00ADD8.svg?logo=go" alt="Go 1.26" /></a>
  <a href="https://github.com/i098/jcyber/blob/main/LICENSE"><img src="https://img.shields.io/badge/license-AGPL%203.0-blue.svg" alt="License: AGPL-3.0" /></a>
  <a href="https://api-docs.deepseek.com/"><img src="https://img.shields.io/badge/engine-DeepSeek%20V4.1-4D6BFE.svg" alt="Engine: DeepSeek V4.1" /></a>
  <a href="http://localhost:8787"><img src="https://img.shields.io/badge/console-localhost%3A8787-22d3ee.svg" alt="Console" /></a>
</p>

> **Use it only on assets you are authorized to test.** jcyber is a pentest
> console, not a scanner you point at the internet.

---

## What is jcyber?

jcyber runs a pentest the way a small team would: a **planner** reads the
state of the engagement and dispatches one-sentence **intents** to **workers**,
each worker explores a single intent against the target, and everything it
confirms — assets, facts, credentials, vulnerabilities — is written back into a
dual knowledge graph (the target's technical topology + the goals of the
engagement). Workers don't just find things; they **verify** them, and only
verified findings get registered.

Around that engine, jcyber is the console a real engagement needs: a
recording **proxy** captures every request so each finding carries the traffic
that proves it, a **report agent** writes the Markdown report the moment a
finding lands, a **retest agent** re-verifies findings claimed fixed, and
**interception rules** hold destructive commands for human approval. It speaks
Anthropic or any OpenAI-compatible API — DeepSeek V4.1 is a good default —
and keeps the whole loop in English.

## The idea behind it

**Verify, then claim.** A finding only exists if the agent actually triggered
it and captured evidence. Version matches and "looks injectable" never count.

**Evidence is the product.** Every request and response is recorded, bound to
the finding it proves, and quoted in the report. A finding without traffic is
a hunch.

**The human stays in the loop.** Chat with the running task, inject hints
mid-flight, and approve the dangerous stuff. Autonomy for the crawling,
judgment for the payload that deletes a row.

**Stealth is a default, not a plugin.** Workers are instructed to look like
an ordinary browser — realistic UAs, cookie reuse, randomized human pacing —
and the console binds to localhost. How loud you are is an operator decision.

## Quickstart

```bash
# 1 · Postgres
docker run -d --name jcyber-pg -p 127.0.0.1:5432:5432 \
  -e POSTGRES_USER=jcyber -e POSTGRES_PASSWORD=jcyber -e POSTGRES_DB=jcyber \
  postgres:16-alpine

# 2 · Build (frontend is embedded into a single binary)
cd web && bun install && bun run build:static && cd ..
mkdir -p server/webui && cp -r web/out server/webui/dist
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -tags embedui -o jcyber ./cmd/apagent

# 3 · Run
JCYBER_PG_DSN='postgres://jcyber:jcyber@127.0.0.1:5432/jcyber?sslmode=disable' \
  ./jcyber -addr 127.0.0.1:8787 -proxy 127.0.0.1:8788
```

Open **http://localhost:8787** — the first visit sets the admin password
(username `jcyber`). Then plug in a model: **System → LLM** → fill provider
(`openai`), base URL, model, and key — e.g. DeepSeek `https://api.deepseek.com`
/ `deepseek-flash` — or just export `ANTHROPIC_API_KEY` before starting.

## Your first pentest

You're staring at the console. Here's the path from zero to a report:

1. **Check the engine.** *System → LLM* should show your profile as active
   (green). If not, create it there — provider, base URL, model, key — and
   hit test.
2. **Create the task.** *Tasks → New Task* (top right). Give it a name and a
   **goal** written like an engagement objective, e.g.
   *"Enumerate http://testphp.example.com and find verified exploitable
   vulnerabilities; register each with evidence."* The goal is what the
   engine decomposes — vague goals wander.
3. **Watch the loop.** Open the task: *Overview* shows the graph of
   intents/facts growing; *Sessions* shows each worker's live tool calls;
   *Coverage* shows which assets have been touched. You are the lead — the
   planner is dispatching, workers are crawling.
4. **Steer if needed.** *Chat* with the task: give a nudge ("focus on the
   API under /api/v2"), add an intent with high priority, or pause it.
   Workers pick up hints on their next round.
5. **Approve the dangerous stuff.** If a worker wants to run something your
   interception rules flag (deletes, writes, state-changing calls), the run
   pauses and the approval appears in *Approval Records* — with the model's
   read of what it does and what happens on success.
6. **Read findings.** *Findings* lists everything verified, severity-tagged,
   with bound traffic evidence. Open one: the Markdown report (written by the
   report agent), reproduction steps, and the request/response pairs that
   prove it.
7. **Retest when fixed.** From a finding's detail page, launch a retest —
   an independent agent re-reads the original evidence and saves its own
   verdict.

Real-time everything: dashboard token usage, *Traffic* for the raw recorded
proxy stream, *Logs* for the backend.

## What's in the box

**The engine**

- ✅ **Dual knowledge graph** — target topology + engagement goals in
  Postgres, event-driven planner over a shared todolist for multi-step chains
- ✅ **Workers with a stealth discipline** — browser-like UAs, cookie reuse,
  randomized human-scale pacing, minimum-viable request counts
- ✅ **Any OpenAI-compatible LLM** — provider pool with health, failover, and
  per-role bindings (e.g. `max` reasoning for workers, `high` for chat)
- ✅ **Skills & MCP** — drop-in `SKILL.md` playbooks (api-recon, playwright)
  and remote MCP servers, editable from the console

**Evidence & reporting**

- ✅ **Recording proxy** — every request/response captured on `:8788` and
  searchable (`traffic_search`), bound to findings in reproduction order
- ✅ **Report agent** — structured Markdown report per finding, saved with the
  evidence version it was written against
- ✅ **Retest agent** — independent re-verification with its own verdict
- ✅ **Findings push** — notify DingTalk / Feishu / WeCom / Telegram /
  Webhook / Email, realtime or digest

**Control plane**

- ✅ **Interception & approvals** — deny/ask rules for destructive commands,
  model-judged fallback approval with full detail cards
- ✅ **Asset sync** — pull assets from ScopeSentry by project or task
- ✅ **One-click update** — SHA256-verified self-update with rollback
- ✅ **Localhost by default** — the console binds `127.0.0.1`; expose it
  only if you mean to

## Configuration

| Env var | Purpose |
|---|---|
| `JCYBER_PG_DSN` | Postgres DSN (required) |
| `JCYBER_LLM_PROVIDER` / `JCYBER_LLM_MODEL` / `JCYBER_LLM_BASE_URL` | LLM defaults (or configure in UI) |
| `JCYBER_LLM_PROXY` | Outbound proxy for LLM traffic |
| `JCYBER_NOTIFY_ALLOW_LOCAL=1` | Allow notification delivery to loopback/private IPs |

Flags: `-addr` (HTTP, default `127.0.0.1:8787`), `-proxy` (recording proxy,
default `127.0.0.1:8788`), `-data` (SQLite/jwt.key dir, default `data/`).

## Development

```bash
go test ./...          # hermetic suite (SQLite)
cd web && bun run check
```

## License

AGPL-3.0 — see [LICENSE](LICENSE). Point it only at assets you're allowed to
test.
