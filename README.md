<p align="center"><img src="web/public/logo.png" height="80" alt="jcyber" /></p>

<h1 align="center">jcyber</h1>

<div align="center">

**Point it at a web app, it maps the thing, verifies what it finds, and writes it up, enjoy!**

[![Go](https://img.shields.io/badge/go-1.26-00ADD8?style=for-the-badge&logo=go)](https://golang.org)
[![License: AGPL-3.0](https://img.shields.io/badge/AGPL--3.0-0d9488?style=for-the-badge)](LICENSE)
[![Stars](https://img.shields.io/github/stars/i098/jcyber?style=for-the-badge&logo=github&color=d97706)](https://github.com/i098/jcyber/stargazers)

[Quick start](#quick-start) · [Your first run](#your-first-run) · [Features](#features) · [How it works](#how-it-works) · [Architecture](ARCHITECTURE.md) · [Phone front desk](PHOTON.md)

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

1. **Clone:**

   ```bash
   git clone https://github.com/i098/jcyber.git && cd jcyber
   ```

2. **Run the script** — it bundles Postgres (an existing container, a new one
   via docker, or a local install if there's no docker) and builds the one
   binary with the console embedded:

   ```bash
   ./install.sh
   ```

3. **Open http://localhost:8787**, set the admin password (username `jcyber`),
   and add a model under **System → LLM** — DeepSeek works well
   (`https://api.deepseek.com`, `deepseek-flash`), or export `ANTHROPIC_API_KEY`
   before starting and the engine picks it up.

The console binds to localhost; pass `-addr :8787` only if you mean to expose it.

For agent-assisted setup on a remote box, give your coding agent this
instruction — it covers everything we ran in practice: bootstrap the VPS, join
the tailnet, configure the engine over an SSH tunnel, and optionally raise the
phone front desk:

```text
Deploy jcyber on a remote Ubuntu VPS. Steps:
1. SSH to the box. Install docker (curl -fsSL https://get.docker.com | sh, needs sudo),
   Go (go.dev/dl tarball into /usr/local), and bun (bun.sh/install).
2. Clone https://github.com/i098/jcyber.git (private: use a GitHub token in the URL).
3. Run ./install.sh — it bundles Postgres (docker container or apt fallback),
   builds the one binary, and starts the console on 127.0.0.1:8787.
4. Install tailscale, run `tailscale up`, and give me the login URL to authorize.
   Once on the tailnet, restart the console bound to the tailscale IP only
   (-addr 100.x.y.z:8787) so the dashboard is never on the public internet.
5. Open an SSH tunnel (-L 18787:127.0.0.1:8787) and configure the console via
   its REST API: POST /api/auth/init to set the admin password, then POST
   /api/llm/profiles for the model profiles (mark one is_default), PUT
   /api/agents/{key}/config with llm_profile_id to bind roles, PUT /api/settings
   for the outbound proxy, POST /api/task-categories + /api/task-templates for a
   starter template. Verify with POST /api/llm/test.
6. Optionally raise the phone front desk: follow PHOTON.md (Photon CLI login,
   shared-line user, run bridge/ with bun against the console URL).
Report each step's result before moving on. Never expose ports on the public IP.
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
