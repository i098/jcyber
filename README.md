<p align="center"><img src="web/public/logo.png" height="80" alt="jcyber" /></p>

<h1 align="center">jcyber</h1>

<div align="center">

**Point it at a web app, it maps the thing, verifies what it finds, and writes it up, enjoy!**

[![Go](https://img.shields.io/badge/go-1.26-00ADD8?style=for-the-badge&logo=go)](https://golang.org)
[![License: AGPL-3.0](https://img.shields.io/badge/AGPL--3.0-0d9488?style=for-the-badge)](LICENSE)
[![Stars](https://img.shields.io/github/stars/i098/jcyber?style=for-the-badge&logo=github&color=d97706)](https://github.com/i098/jcyber/stargazers)

[Quick start](#quick-start) · [Your first run](#your-first-run) · [Features](#features) · [How it works](#how-it-works)

</div>

## Prerequisites

- **A Linux VPS**, 2 GB RAM is plenty. The thinking happens at the model, not
  on this box, so a ~$5/mo machine carries the whole console.
- **Docker, Go 1.26+, and bun or npm** for the bootstrap script. Run the whole
  thing in a container if you want the box isolated from the workers.
- **A model API key** (DeepSeek `deepseek-flash` is a good default).
- **Optional, a rotating residential gateway** (~$2/GB) for when the box's own
  IP shouldn't touch targets. Wire it in under *System Settings → outbound
  proxy*; keep model traffic off it, that's the bulk of the bytes. Web
  requests average ~25 KB, so 1 GB covers about 40,000 of them: a heavy run
  of a million requests lands near $50.

## Quick start

One script does the lot: Postgres in Docker, the one binary built with the
console embedded, then running on http://127.0.0.1:8787. Needs `docker`,
`go` (>= 1.26) and `bun` or `npm` on the machine.

```bash
git clone https://github.com/i098/jcyber.git && cd jcyber
./install.sh
```

Already have Postgres? Skip the container and point the script at it:

```bash
JCYBER_PG_DSN='postgres://user:pass@host:5432/db?sslmode=disable' ./install.sh
```

Open http://localhost:8787 and set the admin password on the first visit
(username `jcyber`). The console binds to localhost; pass `-addr :8787` only
if you mean to expose it.

Plug in a model under **System → LLM**: provider, base URL, model, key.
DeepSeek works well (`https://api.deepseek.com`, `deepseek-flash`), or export
`ANTHROPIC_API_KEY` before starting and the engine picks it up.

For agent-assisted setup, give your coding agent this instruction:

```text
Clone https://github.com/i098/jcyber and run ./install.sh.
Check docker, go and bun (or npm) first and tell me what is missing before
installing anything. When the console is up, confirm http://127.0.0.1:8787
answers and tell me the first-visit steps from README.md.
```

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
