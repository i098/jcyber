# jcyber

<div align="center">

**AI-driven autonomous penetration testing console**

Go monolith backend · Next.js frontend · PostgreSQL

</div>

---

## Features

- **Autonomous exploration** — planner decomposes goals into intents; workers explore, record facts, and register verified findings into a dual knowledge graph (assets + business goals)
- **Evidence-first** — every request captured by the recording proxy (`:8788`) and bindable to findings; findings retest workflow; Markdown vulnerability reports
- **Human in the loop** — chat with agents, inject hints, approval gates for destructive commands
- **Ops surface** — task management, asset coverage graphs, ScopeSentry asset sync, notification channels (DingTalk / Feishu / WeCom / Telegram / Webhook / Email), LLM pool with failover
- **Any LLM** — Anthropic or OpenAI-compatible providers, per-role model binding

## Quick start

```bash
# 1. Postgres
docker run -d --name jcyber-pg -p 127.0.0.1:5432:5432 \
  -e POSTGRES_USER=jcyber -e POSTGRES_PASSWORD=jcyber -e POSTGRES_DB=jcyber \
  postgres:16-alpine

# 2. Build (frontend is embedded into the binary)
cd web && bun install && bun run build:static && cd ..
mkdir -p server/webui && cp -r web/out server/webui/dist
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -tags embedui -o jcyber ./cmd/apagent

# 3. Run
JCYBER_PG_DSN='postgres://jcyber:jcyber@127.0.0.1:5432/jcyber?sslmode=disable' \
  ./jcyber -addr 127.0.0.1:8787 -proxy 127.0.0.1:8788
```

Open http://localhost:8787 — first visit sets the admin password (username `jcyber`).

Set `ANTHROPIC_API_KEY` or `OPENAI_API_KEY` (or configure providers in **System → LLM**).

## Configuration

| Env var | Purpose |
|---|---|
| `JCYBER_PG_DSN` | Postgres DSN (required; no config file needed) |
| `JCYBER_LLM_PROVIDER` / `JCYBER_LLM_MODEL` / `JCYBER_LLM_BASE_URL` | LLM defaults (UI-configurable) |
| `JCYBER_LLM_PROXY` | Outbound proxy for LLM traffic |
| `JCYBER_NOTIFY_ALLOW_LOCAL=1` | Allow notification delivery to loopback/private IPs |

Flags: `-addr` (HTTP listen, defaults `127.0.0.1:8787`), `-proxy` (recording proxy, defaults `127.0.0.1:8788`).

## Operational defaults

- **Stealth discipline built in** — workers are instructed to present as ordinary browsers (realistic UAs, cookie reuse, randomized human-scale pacing, minimum-viable request counts, back-off on rate limits)
- **Localhost by default** — the console binds `127.0.0.1`; pass `-addr :8787` to expose
- **English output** — all findings, reports, and agent communication are in English
- **Segregate infrastructure per engagement** — outbound proxy configurable in System Settings

## Development

```bash
go test ./...                      # hermetic (SQLite) suite
cd web && bun run check            # frontend lint/format
```

## License

AGPL-3.0 — see [LICENSE](LICENSE).
