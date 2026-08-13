.PHONY: infra-up infra-down build dev test fmt lint clean \
        build-rust build-go build-web build-terminal build-quant

# --- Local infra (Postgres, Redpanda, ClickHouse) ---
infra-up:
	docker compose -f infra/docker/docker-compose.yml up -d

infra-down:
	docker compose -f infra/docker/docker-compose.yml down

infra-logs:
	docker compose -f infra/docker/docker-compose.yml logs -f

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

dev-web:
	cd apps/web && npm run dev

dev-terminal:
	cd apps/terminal && npm run tauri dev

# --- Test ---
test:
	cd services/matching-engine && cargo test
	cd services/market-data && cargo test
	cd services/oms-gateway && go test ./...
	cd services/ledger && go test ./...
	cd services/quant-engine && pytest

# --- Lint / format ---
fmt:
	cd services/matching-engine && cargo fmt
	cd services/market-data && cargo fmt
	cd services/oms-gateway && gofmt -w .
	cd services/ledger && gofmt -w .
	cd apps/web && npm run lint -- --fix || true

clean:
	cd services/matching-engine && cargo clean
	cd services/market-data && cargo clean
	find . -name node_modules -type d -prune -exec rm -rf {} +
