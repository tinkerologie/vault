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

.PHONY: all build build-local build-web test test-short test-coverage clean lint security-check deploy verify redeploy deps pre-commit-install pre-commit-run package docker-build docker-push

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
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY)-linux-amd64 ./cmd/vault/

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

# ── Release packaging ──────────────────────────────────────────────────────────

release: build-web
	@echo "Cross-compiling for Linux/amd64..."
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY)-linux-amd64 ./cmd/vault/

package: release
	@echo "Creating plugin package..."
	@mkdir -p $(BUILD_DIR)/pkg/usr/local/sbin
	@mkdir -p $(BUILD_DIR)/pkg/etc/rc.d
	@mkdir -p $(BUILD_DIR)/pkg/usr/local/emhttp/plugins/$(BINARY)
	cp $(BUILD_DIR)/$(BINARY)-linux-amd64 $(BUILD_DIR)/pkg/usr/local/sbin/$(BINARY)
	cp plugin/rc.vault $(BUILD_DIR)/pkg/etc/rc.d/rc.vault
	cp -r plugin/pages/*.page $(BUILD_DIR)/pkg/usr/local/emhttp/plugins/$(BINARY)/
	cp -r plugin/pages/include $(BUILD_DIR)/pkg/usr/local/emhttp/plugins/$(BINARY)/
	cp -r plugin/assets $(BUILD_DIR)/pkg/usr/local/emhttp/plugins/$(BINARY)/
	cd $(BUILD_DIR)/pkg && COPYFILE_DISABLE=1 tar -czf ../$(BINARY)-$(VERSION).tgz usr/ etc/
	@echo "Package created: $(BUILD_DIR)/$(BINARY)-$(VERSION).tgz"

lint:
	golangci-lint run --config .golangci.yml --max-issues-per-linter 0 --max-same-issues 0 ./...

lint-web:
	cd web && npm run lint

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

# ── Docker ────────────────────────────────────────────────────────────────────

docker-build:
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(HASH) --build-arg BUILD_DATE=$(DATE) -t vault-replica:$(VERSION) .

docker-push: docker-build
	docker tag vault-replica:$(VERSION) ruaandeysel/vault-replica:$(VERSION)
	docker tag vault-replica:$(VERSION) ruaandeysel/vault-replica:latest
	docker push ruaandeysel/vault-replica:$(VERSION)
	docker push ruaandeysel/vault-replica:latest
