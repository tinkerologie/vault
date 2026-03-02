VERSION := $(shell cat VERSION)
BINARY := vault
BUILD_DIR := build
GOOS := linux
GOARCH := amd64
DATE := $(shell date '+%Y.%m.%d')
HASH := $(shell git rev-parse --short HEAD 2>/dev/null || echo "dev")

LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.buildDate=$(DATE) \
	-X main.commit=$(HASH)

ANSIBLE_CMD := cd ansible && ansible-playbook -i inventory.yml ansible.yml

.PHONY: all build build-local build-web test test-short test-coverage clean lint security-check deploy verify redeploy deps pre-commit-install pre-commit-run

all: test build-local

# ── Plugin lifecycle (Ansible-driven) ──────────────────────────────────────────

build:
	$(ANSIBLE_CMD) --tags build

deploy:
	$(ANSIBLE_CMD) --tags deploy

verify:
	$(ANSIBLE_CMD) --tags verify

redeploy:
	$(ANSIBLE_CMD) --tags redeploy

# ── Local development utilities ────────────────────────────────────────────────

deps:
	go mod download
	go mod tidy

build-web:
	cd web && npm ci && npm run build

build-local: build-web
	go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY) ./cmd/vault/

test:
	go test ./... -v

test-short:
	go test ./... -short

test-coverage:
	go test ./... -coverprofile=coverage.out -covermode=atomic
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

clean:
	rm -rf $(BUILD_DIR)
	rm -rf web/dist
	rm -f coverage.out coverage.html

lint:
	golangci-lint run --config .golangci.yml --max-issues-per-linter 0 --max-same-issues 0 ./...

security-check:
	gosec -fmt=text -exclude-dir=vendor -exclude-dir=build -exclude=G104,G106,G110,G115,G117,G204,G301,G304,G305,G306,G703 -severity=medium -confidence=medium ./...
	govulncheck ./...
	go mod verify

pre-commit-install:
	@echo "Installing pre-commit hooks..."
	pre-commit install
	pre-commit install --hook-type commit-msg

pre-commit-run:
	pre-commit run --all-files
