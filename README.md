# jcyber

AI-driven autonomous penetration testing console (Go backend + Next.js frontend).

## Quick start (local)

```bash
# Postgres
docker run -d --name jcyber-pg -p 127.0.0.1:5432:5432 \
  -e POSTGRES_USER=jcyber -e POSTGRES_PASSWORD=jcyber -e POSTGRES_DB=jcyber \
  postgres:16-alpine

# Build (frontend is embedded into the binary)
cd web && bun install && bun run build:static && cd ..
mkdir -p server/webui && cp -r web/out server/webui/dist
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -tags embedui -o jcyber ./cmd/apagent

# Run (dashboard binds to localhost by default)
JCYBER_PG_DSN='postgres://jcyber:jcyber@127.0.0.1:5432/jcyber?sslmode=disable' \
  ./jcyber -addr 127.0.0.1:8787 -proxy 127.0.0.1:8788
```

Open http://localhost:8787 — first visit sets the admin password (username `jcyber`).

LLM keys (`ANTHROPIC_API_KEY` / `OPENAI_API_KEY`, or configure in the UI under System > LLM).

## Operational defaults

- Dashboard binds to `127.0.0.1` (pass `-addr :8787` to expose).
- Workers carry a code-fixed stealth discipline: browser-like UAs, cookie reuse,
  randomized human-scale pacing, minimum-viable request counts.
- Outbound proxy configurable in System Settings; keep engagement infrastructure
  segregated per client.

## License

AGPL-3.0; see [LICENSE](LICENSE).
