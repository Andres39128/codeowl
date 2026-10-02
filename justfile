# =============================================================================
# codeowl — runner de tareas del monorepo
# Guía §2.2: "Todo comando de desarrollo vive en el justfile — nadie memoriza
# comandos: just --list". §3.2 declara las recetas: dev, test, lint, migrate,
# up, down, deploy. §6 F0: `just dev` levanta todo (compose.dev en desarrollo).
#
# Requiere un .env en la raíz: cp deploy/.env.example .env
# =============================================================================

set dotenv-load := true

compose := `command -v podman >/dev/null 2>&1 && echo "podman compose" || echo "docker compose"`
compose_file := "deploy/compose.dev.yml"

# lista las recetas disponibles
default:
    @just --list

# levanta todo en desarrollo: postgres (compose) + api y worker con go run (§6 F0)
dev: up
    #!/usr/bin/env bash
    set -e
    trap 'kill 0' EXIT
    (cd backend && go run ./cmd/api) &
    (cd backend && go run ./cmd/worker) &
    wait

# baja el stack de desarrollo
down:
    {{ compose }} --env-file .env -f {{ compose_file }} down

# sube postgres con pgvector (mapa servicios.postgres)
up:
    {{ compose }} --env-file .env -f {{ compose_file }} up -d

# compila backend y/o dashboard — just build [backend|dashboard|all]
build target="all":
    @if [ "{{ target }}" = "all" ] || [ "{{ target }}" = "backend" ]; then cd backend && go build -o bin/ ./cmd/api ./cmd/worker; fi
    @if [ "{{ target }}" = "all" ] || [ "{{ target }}" = "dashboard" ]; then pnpm --dir dashboard build; fi

# construye la imagen sandbox del analyzer (§9.4) — la invoca internal/analyze
analyzer-build:
    podman build -t localhost/codeowl-analyzer:latest -f analyzer/Containerfile analyzer/

# lints backend y/o dashboard — just lint [backend|dashboard|all] (§4.5)
lint target="all":
    @if [ "{{ target }}" = "all" ] || [ "{{ target }}" = "backend" ]; then cd backend && golangci-lint run; fi
    @if [ "{{ target }}" = "all" ] || [ "{{ target }}" = "dashboard" ]; then pnpm --dir dashboard lint; fi

# tests backend y/o dashboard — just test [backend|dashboard|all] (§8)
test target="all":
    @if [ "{{ target }}" = "all" ] || [ "{{ target }}" = "backend" ]; then cd backend && go test ./...; fi
    @if [ "{{ target }}" = "all" ] || [ "{{ target }}" = "dashboard" ]; then pnpm --dir dashboard test; fi

# aplica migraciones (golang-migrate, §3.3) contra DATABASE_URL — just migrate [up|down]
migrate direction="up":
    #!/usr/bin/env bash
    set -euo pipefail
    command -v migrate >/dev/null 2>&1 || { echo "falta el CLI de golang-migrate — instalalo por fuera (guía §2.2: sqlc + golang-migrate)"; exit 127; }
    migrate -path backend/migrations -database "$DATABASE_URL" {{ direction }}

# Requiere CODEOWL_DEPLOY_HOST=usuario@vps. El env y los secrets del VPS
# (%h/opt/codeowl/env y secrets/) se gestionan a mano — nunca viajan por rsync.
# Caddy y Postgres no se reinician en un deploy normal.

# despliegue al VPS: construye, sube, migra, reconstruye analyzer y reinicia (§9.13)
deploy:
    #!/usr/bin/env bash
    set -euo pipefail
    : "${CODEOWL_DEPLOY_HOST:?definí CODEOWL_DEPLOY_HOST=usuario@vps (guía §9.13)}"
    (cd backend && GOOS=linux GOARCH="${GOARCH:-amd64}" go build -o bin/ ./cmd/api ./cmd/worker)
    ssh "$CODEOWL_DEPLOY_HOST" "mkdir -p opt/codeowl/bin opt/codeowl/backend opt/codeowl/analyzer"
    rsync backend/bin/api backend/bin/worker "$CODEOWL_DEPLOY_HOST:opt/codeowl/bin/"
    rsync -a backend/migrations/ "$CODEOWL_DEPLOY_HOST:opt/codeowl/backend/migrations/"
    rsync -a analyzer/ "$CODEOWL_DEPLOY_HOST:opt/codeowl/analyzer/"
    ssh "$CODEOWL_DEPLOY_HOST" 'set -a; . ~/opt/codeowl/env; set +a; migrate -path ~/opt/codeowl/backend/migrations -database "$DATABASE_URL" up'
    ssh "$CODEOWL_DEPLOY_HOST" 'systemctl --user start analyzer-build.service'
    ssh "$CODEOWL_DEPLOY_HOST" 'systemctl --user restart api worker'
