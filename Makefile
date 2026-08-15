.PHONY: infra-up infra-down infra-down-clean infra-logs infra-status infra-disk \
        build dev test fmt lint clean \
        build-rust build-go build-web build-terminal build-quant \
        dev-matching-engine dev-market-data dev-oms-gateway dev-ledger \
        dev-auth dev-backoffice dev-kyc-onboarding dev-api-gateway \
        dev-reporting dev-mutual-funds dev-quant-engine dev-web dev-terminal

# --- Local infra (Postgres, Redpanda, ClickHouse) ---
# Resource caps live in infra/docker/docker-compose.yml (mem_limit/cpus/pids_limit
# per service + log rotation). Docker Desktop's own VM CPU/memory/disk-image-size
# ceiling (Settings > Resources) is the actual backstop above those — set that
# once, by hand, outside this Makefile.

# Warn (don't block) if Docker's total disk usage already looks large before
# adding more on top of it.
DISK_WARN_THRESHOLD_GB := 20

infra-up:
	@reclaimable_gb=$$(docker system df --format '{{.Size}}' 2>/dev/null | head -1 | sed -E 's/[^0-9.]*//g' | cut -d. -f1); \
	if [ -n "$$reclaimable_gb" ] && [ "$$reclaimable_gb" -ge $(DISK_WARN_THRESHOLD_GB) ] 2>/dev/null; then \
		echo "⚠️  Docker is already using ~$${reclaimable_gb}GB — consider 'make infra-disk' then 'docker system prune' before starting more."; \
	fi
	docker compose -f infra/docker/docker-compose.yml up -d

infra-down:
	docker compose -f infra/docker/docker-compose.yml down

# Also removes the bind-mounted ./infra/docker/volumes/* data (Postgres/Redpanda/
# ClickHouse state) — plain `infra-down` leaves that on disk indefinitely.
infra-down-clean:
	docker compose -f infra/docker/docker-compose.yml down -v

infra-logs:
	docker compose -f infra/docker/docker-compose.yml logs -f

# Live CPU/mem/pid usage for this compose project's containers, checked against
# the caps set in docker-compose.yml.
infra-status:
	docker compose -f infra/docker/docker-compose.yml ps -q | xargs -r docker stats --no-stream

# Disk usage: overall Docker footprint, then this project's bind-mounted volumes.
infra-disk:
	docker system df -v
	@echo "--- infra/docker/volumes ---"
	@du -sh infra/docker/volumes/* 2>/dev/null || echo "(no volumes yet — run 'make infra-up' first)"

# --- Build everything ---
build: build-rust build-go build-web build-quant

build-rust:
	cd services/matching-engine && cargo build
	cd services/market-data && cargo build

build-go:
	cd services/oms-gateway && go build ./...
	cd services/ledger && go build ./...
	cd services/kyc-onboarding && go build ./...
	cd services/backoffice && go build ./...
	cd services/auth && go build ./...
	cd services/api-gateway && go build ./...
	cd services/reporting && go build ./...
	cd services/mutual-funds && go build ./...

build-web:
	cd apps/web && npm install && npm run build

build-terminal:
	cd apps/terminal && npm install && npm run tauri build

build-quant:
	cd services/quant-engine && pip install -e .

# --- Run each service locally (foreground; run in separate terminals) ---
dev-matching-engine:
	cd services/matching-engine && cargo run

dev-market-data:
	cd services/market-data && cargo run

dev-oms-gateway:
	cd services/oms-gateway && go run ./cmd/server

dev-ledger:
	cd services/ledger && go run ./cmd/server

dev-auth:
	cd services/auth && go run ./cmd/server

dev-backoffice:
	cd services/backoffice && go run ./cmd/server

dev-kyc-onboarding:
	cd services/kyc-onboarding && go run ./cmd/server

dev-api-gateway:
	cd services/api-gateway && go run ./cmd/server

dev-reporting:
	cd services/reporting && go run ./cmd/server

dev-mutual-funds:
	cd services/mutual-funds && go run ./cmd/server

dev-quant-engine:
	cd services/quant-engine && quant-engine-server

dev-web:
	cd apps/web && npm run dev

dev-terminal:
	cd apps/terminal && npm run tauri dev

# --- Test ---
# apps/web has no "test" script in package.json as of this writing (only
# lint/build) — deliberately not invoked here to avoid a hard failure;
# see docs/SETUP.md's "Running tests" section for that gap.
test:
	cd services/matching-engine && cargo test
	cd services/market-data && cargo test
	cd services/oms-gateway && go test ./...
	cd services/ledger && go test ./...
	cd services/auth && go test ./...
	cd services/backoffice && go test ./...
	cd services/kyc-onboarding && go test ./...
	cd services/api-gateway && go test ./...
	cd services/reporting && go test ./...
	cd services/mutual-funds && go test ./...
	cd services/quant-engine && pytest
	cd apps/terminal && npm install && npm run test

# --- Lint / format ---
fmt:
	cd services/matching-engine && cargo fmt
	cd services/market-data && cargo fmt
	cd services/oms-gateway && gofmt -w .
	cd services/ledger && gofmt -w .
	cd services/auth && gofmt -w .
	cd services/backoffice && gofmt -w .
	cd services/kyc-onboarding && gofmt -w .
	cd services/api-gateway && gofmt -w .
	cd services/reporting && gofmt -w .
	cd services/mutual-funds && gofmt -w .
	cd apps/web && npm run lint -- --fix || true

clean:
	cd services/matching-engine && cargo clean
	cd services/market-data && cargo clean
	find . -name node_modules -type d -prune -exec rm -rf {} +
