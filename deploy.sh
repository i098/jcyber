#!/usr/bin/env bash
# jcyber one-command deploy for a fresh Ubuntu VPS.
# Usage: GITHUB_TOKEN=<pat> ./deploy.sh   (token needs repo read access to clone)
set -euo pipefail
cd "$(cd "$(dirname "$0")" && pwd)"

info(){ printf '\033[36m[*]\033[0m %s\n' "$*"; }
ok(){   printf '\033[32m[+]\033[0m %s\n' "$*"; }
die(){  printf '\033[31m[x]\033[0m %s\n' "$*" >&2; exit 1; }

# ── deps ────────────────────────────────────────────────────
if ! command -v docker >/dev/null; then
  info "installing docker"
  curl -fsSL https://get.docker.com | sh
fi
if ! command -v go >/dev/null; then
  info "installing go"
  curl -fsSL https://go.dev/dl/go1.26.5.linux-amd64.tar.gz | tar -C /usr/local -xz
  export PATH=$PATH:/usr/local/go/bin
  grep -q '/usr/local/go/bin' ~/.profile || echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.profile
fi
if ! command -v bun >/dev/null; then
  info "installing bun"
  curl -fsSL https://bun.sh/install | bash
  export PATH=$HOME/.bun/bin:$PATH
fi
ok "deps ready"

# ── source ────────────────────────────────────────────────────
if [ ! -d jcyber ]; then
  [ -n "${GITHUB_TOKEN:-}" ] || die "GITHUB_TOKEN required to clone the private repo"
  git clone -q "https://${GITHUB_TOKEN}@github.com/i098/jcyber.git"
fi
cd jcyber

# ── postgres + run ────────────────────────────────────────────
docker run -d --name jcyber-pg -p 127.0.0.1:5432:5432 \
  -e POSTGRES_USER=jcyber -e POSTGRES_PASSWORD=jcyber -e POSTGRES_DB=jcyber \
  postgres:16-alpine || docker start jcyber-pg
for i in $(seq 1 30); do docker exec jcyber-pg pg_isready -U jcyber >/dev/null 2>&1 && break; sleep 1; done

cd web && bun install && bun run build:static && cd ..
mkdir -p server/webui && rm -rf server/webui/dist && cp -r web/out server/webui/dist
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -tags embedui -o jcyber ./cmd/apagent
ok "built ./jcyber"

exec ./install.sh
