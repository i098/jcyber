#!/usr/bin/env bash
# jcyber bootstrap: Postgres (docker) + build the binary + run the console.
set -euo pipefail
cd "$(cd "$(dirname "$0")" && pwd)"

info(){ printf '\033[36m[*]\033[0m %s\n' "$*"; }
ok(){   printf '\033[32m[+]\033[0m %s\n' "$*"; }
warn(){ printf '\033[33m[!]\033[0m %s\n' "$*"; }
die(){  printf '\033[31m[x]\033[0m %s\n' "$*" >&2; exit 1; }

DSN="${JCYBER_PG_DSN:-}"

# ── Postgres ───────────────────────────────────────────────
if [ -z "$DSN" ]; then
  if command -v docker >/dev/null 2>&1; then
    if docker ps -a --format '{{.Names}}' | grep -qx jcyber-pg; then
      docker start jcyber-pg >/dev/null
      ok "reusing container jcyber-pg"
    elif ! nc -z 127.0.0.1 5432 2>/dev/null && ! docker run -d --name jcyber-pg \
        -p 127.0.0.1:5432:5432 \
        -e POSTGRES_USER=jcyber -e POSTGRES_PASSWORD=jcyber -e POSTGRES_DB=jcyber \
        postgres:16-alpine >/dev/null; then
      die "could not start jcyber-pg; is port 5432 already in use?"
    else
      ok "started jcyber-pg"
    fi
    DSN='postgres://jcyber:jcyber@127.0.0.1:5432/jcyber?sslmode=disable'
    info "waiting for Postgres"
    for i in $(seq 1 30); do
      docker exec jcyber-pg pg_isready -U jcyber -d jcyber >/dev/null 2>&1 && break
      sleep 1
    done
  else
    die "no docker for Postgres; set JCYBER_PG_DSN to an existing server"
  fi
fi

# ── toolchain ───────────────────────────────────────────────
command -v go >/dev/null 2>&1 || die "Go >= 1.26 required: https://go.dev/dl/"

if command -v bun >/dev/null 2>&1; then
  ( cd web && bun install && bun run build:static )
elif command -v npm >/dev/null 2>&1; then
  ( cd web && npm ci && npm run build:static )
else
  die "bun or npm required to build the console"
fi

mkdir -p server/webui
rm -rf server/webui/dist
cp -r web/out server/webui/dist

CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -tags embedui -o jcyber ./cmd/apagent
ok "built ./jcyber"

# ── run ─────────────────────────────────────────────────────
info "starting console on http://127.0.0.1:8787 (Ctrl-C to stop)"
JCYBER_PG_DSN="$DSN" exec ./jcyber -addr 127.0.0.1:8787 -proxy 127.0.0.1:8788
