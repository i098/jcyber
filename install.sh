#!/usr/bin/env bash
# jcyber bootstrap: bundles Postgres, builds the binary, runs the console.
set -euo pipefail
cd "$(cd "$(dirname "$0")" && pwd)"

info(){ printf '\033[36m[*]\033[0m %s\n' "$*"; }
ok(){   printf '\033[32m[+]\033[0m %s\n' "$*"; }
die(){  printf '\033[31m[x]\033[0m %s\n' "$*" >&2; exit 1; }
S(){ echo "$INSTALL_SUDO_PW" | sudo -S -p "" sh -c "$*"; }
INSTALL_SUDO_PW="${INSTALL_SUDO_PW:-}"
need_sudo(){ [ "$(id -u)" = 0 ] || { [ -n "$INSTALL_SUDO_PW" ] || die "set INSTALL_SUDO_PW=<password> for sudo steps (Postgres install)"; } }

# ── Postgres, bundled: env DSN > existing container > docker run > apt install ──
DSN="${JCYBER_PG_DSN:-}"
if [ -z "$DSN" ]; then
  if command -v docker >/dev/null 2>&1; then
    if docker ps -a --format '{{.Names}}' | grep -qx jcyber-pg; then
      docker start jcyber-pg >/dev/null
      ok "reusing container jcyber-pg"
    else
      docker run -d --name jcyber-pg -p 127.0.0.1:5432:5432 \
        -e POSTGRES_USER=jcyber -e POSTGRES_PASSWORD=jcyber -e POSTGRES_DB=jcyber \
        postgres:16-alpine >/dev/null
      ok "started jcyber-pg container"
    fi
    DSN='postgres://jcyber:jcyber@127.0.0.1:5432/jcyber?sslmode=disable'
    info "waiting for Postgres"
    for i in $(seq 1 30); do
      docker exec jcyber-pg pg_isready -U jcyber -d jcyber >/dev/null 2>&1 && break
      sleep 1
    done
  elif command -v psql >/dev/null 2>&1 || [ "$(uname -s)" = "Linux" ]; then
    # no docker: install a local Postgres the boring way
    command -v psql >/dev/null 2>&1 || { need_sudo; S "apt-get update -qq && apt-get install -y -qq postgresql"; }
    need_sudo
    S "systemctl enable --now postgresql" || true
    S "su postgres -c \"psql -tc \\\"SELECT 1 FROM pg_roles WHERE rolname='jcyber'\\\" | grep -q 1 || psql -c \\\"CREATE USER jcyber WITH PASSWORD 'jcyber' CREATEDB\\\"\"" || true
    S "su postgres -c \"psql -tc \\\"SELECT 1 FROM pg_database WHERE datname='jcyber'\\\" | grep -q 1 || createdb -O jcyber jcyber\"" || true
    S "pg_isready" >/dev/null 2>&1 || S "service postgresql start" || true
    DSN='postgres://jcyber:jcyber@127.0.0.1:5432/jcyber?sslmode=disable'
    ok "local Postgres ready (user/db: jcyber)"
  else
    die "no docker and not Linux; set JCYBER_PG_DSN to an existing Postgres"
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

CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -tags embedui -o jcyber ./cmd/jcyber
ok "built ./jcyber"

# ── run ─────────────────────────────────────────────────────
info "starting console on http://127.0.0.1:8787 (Ctrl-C to stop)"
JCYBER_PG_DSN="$DSN" exec ./jcyber -addr 127.0.0.1:8787 -proxy 127.0.0.1:8788
