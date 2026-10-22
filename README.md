<p align="center"><img src="web/public/logo.png" height="80" alt="jcyber" /></p>

<h1 align="center">jcyber</h1>

<div align="center">

**Point it at a web app, it maps the thing, verifies what it finds, and writes it up, enjoy!**

[![Go](https://img.shields.io/badge/go-1.26-00ADD8?style=for-the-badge&logo=go)](https://golang.org)
[![License: AGPL-3.0](https://img.shields.io/badge/AGPL--3.0-0d9488?style=for-the-badge)](LICENSE)
[![Stars](https://img.shields.io/github/stars/i098/jcyber?style=for-the-badge&logo=github&color=d97706)](https://github.com/i098/jcyber/stargazers)

[Quick start](#quick-start) · [Your first run](#your-first-run) · [Features](#features) · [How it works](#how-it-works)

</div>

## Quick start

Do you already have Postgres?

**No:** run this on your machine:

```bash
docker run -d --name jcyber-pg -p 127.0.0.1:5432:5432 \
  -e POSTGRES_USER=jcyber -e POSTGRES_PASSWORD=jcyber -e POSTGRES_DB=jcyber \
  postgres:16-alpine
```

**Yes:** skip that and point the DSN in the run command at it.

Then build the one binary, frontend included, and start it:

```bash
cd web && bun install && bun run build:static && cd ..
mkdir -p server/webui && cp -r web/out server/webui/dist
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -tags embedui -o jcyber ./cmd/apagent

JCYBER_PG_DSN='postgres://jcyber:jcyber@127.0.0.1:5432/jcyber?sslmode=disable' \
  ./jcyber -addr 127.0.0.1:8787 -proxy 127.0.0.1:8788
```

Open http://localhost:8787 and set the admin password on the first visit
(username `jcyber`). The console binds to localhost; pass `-addr :8787` only
if you mean to expose it.

Plug in a model under **System → LLM**: provider, base URL, model, key.
DeepSeek works well (`https://api.deepseek.com`, `deepseek-flash`), or export
`ANTHROPIC_API_KEY` before starting and the engine picks it up.

## Your first run

1. **Check the engine.** *System → LLM* shows your profile as active, in
   green. If not, fill the form there and hit test.
2. **Create a task.** *Tasks → New Task*, top right. Write the goal like an
   outcome, not a wish: "Map http://testphp.example.com and register verified
   findings with evidence." The engine decomposes the goal; vague goals
   wander.
3. **Watch the loop.** *Overview* shows the graph of intents and facts
   growing. *Sessions* shows each worker's live tool calls. *Coverage* shows
   which assets got touched.
4. **Steer.** *Chat* with the task: nudge it ("focus on the API under
   /api/v2"), add an intent with high priority, or pause it. Workers pick up
   hints on their next round.
5. **Approve the risky stuff.** A worker wanting to run something the rules
   flag (deletes, writes, state-changing calls) pauses, and the call lands in
   *Approval Records* with the model's read of what it does.
6. **Read the findings.** *Findings* lists everything verified, severity-tagged.
   Open one for the Markdown writeup, the steps to reproduce it, and the
   request and response pairs that prove it.
7. **Re-verify.** From a finding's page, launch a retest: an independent agent
   re-reads the original evidence and saves its own verdict.

## Features

- **Planner and workers:** an event-driven planner hands one-sentence intents to workers.
- **Dual knowledge graph:** target topology and task goals in Postgres; chains keep order.
- **Evidence-first findings:** every request and response is recorded and bound to its finding.
- **Reports and retests:** a report agent writes the writeup; a retest agent re-verifies it.
- **Stealth discipline:** workers look like an ordinary browser: realistic UAs, cookies, pacing.
- **Any OpenAI-compatible model:** provider pool, failover, per-role reasoning effort.
- **Approvals:** deny or ask rules hold state-changing commands for a human.
- **Skills and MCP:** drop-in `SKILL.md` playbooks and remote MCP servers, editable in console.
- **Asset sync:** pull assets from ScopeSentry by project or task.
- **One-click update:** SHA256-verified self-update with rollback.

## How it works

A planner reads the task graph and hands one-sentence intents to workers. Each
worker takes one intent, exercises the target, and writes what it confirms back
to the graph. A finding only counts when the worker actually triggered it and
captured the evidence; version matches never count. Every request flows through
the recording proxy on `:8788`, so a finding carries the request and response
that prove it, in reproduction order. You watch from the console, steer through
chat, and approve whatever the rules flag.

## Repository layout

- [`agent/`](agent/): planner, workers, prompt catalog, tools
- [`server/`](server/): HTTP API, orchestration, console backend
- [`db/`](db/): Postgres schema and stores
- [`traffic/`](traffic/): recording proxy and evidence
- [`skills/`](skills/): drop-in `SKILL.md` playbooks
- [`web/`](web/): the Next.js console
- [`cmd/apagent/`](cmd/apagent/): entrypoint

## Built with

Built on Go, Next.js, and AGPL-3.0 open-source components: see
[NOTICE](NOTICE).

## License

AGPL-3.0, see [LICENSE](LICENSE).
