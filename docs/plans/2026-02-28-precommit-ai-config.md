# Pre-commit & AI Configuration Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:executing-plans to implement this plan task-by-task.

**Goal:** Add pre-commit hooks, golangci-lint config, and AI assistant files (AGENTS.md, instructions, prompts) adapted from unraid-management-agent.

**Architecture:** Mirror the reference project's developer tooling structure while adapting all content for Vault's layered backup daemon architecture (CLI → API → Handlers → DB/Storage/Engine).

**Tech Stack:** pre-commit framework, golangci-lint v2, gosec, govulncheck, prettier, markdownlint, shellcheck, detect-secrets, codespell

---

### Task 0: Install Required Tools

**Files:**

- Create: `scripts/setup-pre-commit.sh`

**Step 1: Create the setup script**

```bash
#!/bin/bash

# Pre-commit Setup Script for Vault
# Automates installation of pre-commit hooks and all required tools

set -e

echo "Setting up pre-commit hooks for Vault..."
echo ""

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

if [ ! -d ".git" ]; then
    echo -e "${RED}Error: This script must be run from the root of the git repository${NC}"
    exit 1
fi

command_exists() {
    command -v "$1" >/dev/null 2>&1
}

# Check and install Python
echo "Checking dependencies..."
if ! command_exists python3; then
    echo -e "${RED}Python 3 is not installed${NC}"
    echo "Install with: brew install python3 (macOS) or apt-get install python3 python3-pip (Linux)"
    exit 1
fi
echo -e "${GREEN}✓ Python 3 found${NC}"

# Check and install pip
if ! command_exists pip3 && ! command_exists pip; then
    echo -e "${YELLOW}pip not found, installing...${NC}"
    if [[ "$OSTYPE" == "darwin"* ]]; then
        brew install python3 || { echo -e "${RED}Failed to install pip${NC}"; exit 1; }
    else
        sudo apt-get update && sudo apt-get install -y python3-pip || { echo -e "${RED}Failed to install pip${NC}"; exit 1; }
    fi
fi
echo -e "${GREEN}✓ pip found${NC}"

# Install pre-commit
echo ""
echo "Installing pre-commit..."
if ! command_exists pre-commit; then
    pip3 install pre-commit --user || pip install pre-commit --user || { echo -e "${RED}Failed to install pre-commit${NC}"; exit 1; }
    if ! command_exists pre-commit; then
        export PATH="$HOME/.local/bin:$PATH"
        echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.bashrc
    fi
fi
echo -e "${GREEN}✓ pre-commit installed${NC}"

# Check Go
echo ""
echo "Checking Go installation..."
if ! command_exists go; then
    echo -e "${RED}Go is not installed${NC}"
    echo "Install from: https://go.dev/dl/"
    exit 1
fi
GO_VERSION=$(go version | awk '{print $3}')
echo -e "${GREEN}✓ Go ${GO_VERSION} found${NC}"

# Install Go tools
echo ""
echo "Installing Go development tools..."

tools=(
    "github.com/golangci/golangci-lint/cmd/golangci-lint@latest"
    "github.com/securego/gosec/v2/cmd/gosec@latest"
    "golang.org/x/vuln/cmd/govulncheck@latest"
)

for tool in "${tools[@]}"; do
    tool_name=$(basename "${tool%%@*}")
    if command_exists "$tool_name"; then
        echo -e "  ${GREEN}✓ ${tool_name} already installed${NC}"
    else
        echo "  → Installing ${tool_name}..."
        go install "$tool" || echo -e "${YELLOW}Failed to install ${tool_name}, continuing...${NC}"
    fi
done
echo -e "${GREEN}✓ Go tools installed${NC}"

# Install Node.js tools (prettier, markdownlint)
echo ""
echo "Checking Node.js tools..."
if ! command_exists node; then
    echo -e "${YELLOW}Node.js not found. Installing via brew...${NC}"
    if [[ "$OSTYPE" == "darwin"* ]]; then
        brew install node || echo -e "${YELLOW}Failed to install node, prettier/markdownlint hooks will use pre-commit's built-in node${NC}"
    else
        echo -e "${YELLOW}Install Node.js manually for prettier/markdownlint support${NC}"
    fi
fi

# Install pre-commit hooks
echo ""
echo "Installing pre-commit hooks..."
pre-commit install || { echo -e "${RED}Failed to install pre-commit hooks${NC}"; exit 1; }
pre-commit install --hook-type commit-msg || echo -e "${YELLOW}Failed to install commit-msg hook, continuing...${NC}"
echo -e "${GREEN}✓ Pre-commit hooks installed${NC}"

# Install hook dependencies
echo ""
echo "Installing pre-commit hook dependencies..."
pre-commit install-hooks || echo -e "${YELLOW}Some hooks may not have installed correctly${NC}"

# Create secrets baseline if it doesn't exist
if [ ! -f ".secrets.baseline" ]; then
    echo ""
    echo "Creating secrets baseline..."
    if command_exists detect-secrets; then
        detect-secrets scan --baseline .secrets.baseline
    else
        echo '{"version": "1.5.0", "plugins_used": [], "filters_used": [], "results": {}}' > .secrets.baseline
    fi
    echo -e "${GREEN}✓ Secrets baseline created${NC}"
fi

# Run smoke test
echo ""
echo "Running pre-commit checks on all files..."
if pre-commit run --all-files; then
    echo -e "${GREEN}✓ All pre-commit checks passed!${NC}"
else
    echo -e "${YELLOW}Some checks failed. This is normal for first-time setup.${NC}"
    echo -e "${YELLOW}Run 'make pre-commit-run' to see details and fix issues.${NC}"
fi

echo ""
echo "═══════════════════════════════════════════════════════════"
echo -e "${GREEN}Pre-commit setup complete!${NC}"
echo "═══════════════════════════════════════════════════════════"
echo ""
echo "What's next:"
echo "  • Pre-commit will now run automatically on git commit"
echo "  • Run 'make pre-commit-run' to check all files manually"
echo "  • Run 'make lint' for just linting checks"
echo "  • Run 'make security-check' for security scans"
echo ""
echo "Zero Tolerance Policy:"
echo "  • No linting warnings or errors allowed"
echo "  • No security vulnerabilities (medium+ severity)"
echo "  • Code must be properly formatted"
```

Write to `scripts/setup-pre-commit.sh` and make it executable: `chmod +x scripts/setup-pre-commit.sh`

**Step 2: Commit**

```bash
git add scripts/setup-pre-commit.sh
git commit -m "feat: add pre-commit setup script with auto tool installation"
```

---

### Task 1: Create golangci-lint Configuration

**Files:**

- Create: `.golangci.yml`

**Step 1: Create the config file**

```yaml
# golangci-lint v2 configuration
# Run: golangci-lint run --config=.golangci.yml ./...

version: 2

linters:
  enable:
    - errcheck
    - govet
    - ineffassign
    - staticcheck
    - unused
    - misspell
    - gosec
    - unconvert

formatters:
  enable:
    - gofmt
    - goimports

linters-settings:
  gocyclo:
    min-complexity: 200

  errcheck:
    check-blank: false
    check-type-assertions: false
    exclude-functions:
      - (io.Closer).Close
      - (*os.File).Close
      - (net.Conn).Close
      - strconv.ParseUint
      - strconv.ParseInt
      - strconv.ParseFloat
      - strconv.Atoi
      - fmt.Sscanf
      - fmt.Fprintf
      - json.Marshal
      - json.MarshalIndent
      - (http.ResponseWriter).Write
      - url.Parse
      - os.Remove
      - os.RemoveAll
      - os.Chmod

  govet:
    enable-all: true
    disable:
      - fieldalignment
      - shadow

  gosec:
    excludes:
      - G204
      - G304
      - G115
      - G104
      - G301
      - G306

run:
  timeout: 5m
  tests: false

issues:
  max-issues-per-linter: 0
  max-same-issues: 0
  new: false
  exclude-dirs:
    - build
    - ansible
    - plugin
  exclude-rules:
    - linters:
        - staticcheck
      text: "(SA1019).*(NewClientWithOpts|WithAPIVersionNegotiation)"
    - linters:
        - staticcheck
      text: "QF1008"

output:
  formats:
    colored-line-number: {}
  print-issued-lines: true
  print-linter-name: true
  sort-results: true
```

**Step 2: Run lint to verify config works**

Run: `golangci-lint run --config=.golangci.yml ./...`
Expected: Either PASS or specific issues that need `.golangci.yml` exclusion tuning.

**Step 3: Fix any lint issues or add exclusions until it passes**

**Step 4: Commit**

```bash
git add .golangci.yml
git commit -m "feat: add golangci-lint v2 configuration"
```

---

### Task 2: Create Pre-commit Configuration

**Files:**

- Create: `.pre-commit-config.yaml`

**Step 1: Create the pre-commit config**

```yaml
---
# Pre-commit hooks configuration
# Install: pip install pre-commit && pre-commit install
# Run manually: pre-commit run --all-files

default_language_version:
  python: python3

repos:
  # General file checks
  - repo: https://github.com/pre-commit/pre-commit-hooks
    rev: v5.0.0
    hooks:
      - id: trailing-whitespace
        exclude: '^(.*\.md|.*\.txt)$'
      - id: end-of-file-fixer
        exclude: '^(.*\.txt)$'
      - id: check-yaml
        args: ["--unsafe"]
      - id: check-json
      - id: check-toml
      - id: check-added-large-files
        args: ["--maxkb=1000"]
      - id: check-merge-conflict
      - id: check-case-conflict
      - id: mixed-line-ending
        args: ["--fix=lf"]
      - id: detect-private-key

  # Go formatting
  - repo: https://github.com/dnephin/pre-commit-golang
    rev: v0.5.1
    hooks:
      - id: go-fmt
        name: Go Format (gofmt)
        args: ["-s", "-w"]
        files: '\.go$'
        exclude: "^vendor/"
      - id: go-imports
        name: Go Imports (goimports)
        files: '\.go$'
        exclude: "^vendor/"
      - id: go-vet
        name: Go Vet
        files: '\.go$'
        exclude: "^(vendor/|tests/)"

  # Go build
  - repo: local
    hooks:
      - id: go-build
        name: Go Build (linux/amd64)
        description: Verify code compiles for target platform
        entry: bash -c 'CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /dev/null ./cmd/vault/'
        language: system
        pass_filenames: false
        files: '\.go$'

  # Go mod tidy
  - repo: https://github.com/dnephin/pre-commit-golang
    rev: v0.5.1
    hooks:
      - id: go-mod-tidy
        name: Go Mod Tidy

  # golangci-lint
  - repo: local
    hooks:
      - id: golangci-lint
        name: golangci-lint (Zero Tolerance)
        entry: bash -c 'if command -v golangci-lint >/dev/null 2>&1; then golangci-lint run --timeout=5m --config=.golangci.yml --max-issues-per-linter=0 --max-same-issues=0 --issues-exit-code=1 ./...; else echo "golangci-lint not installed. Run: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest"; exit 1; fi'
        types: [go]
        language: system
        pass_filenames: false

  # gosec
  - repo: local
    hooks:
      - id: gosec
        name: GoSec Security Scanner
        entry: bash -c 'if command -v gosec >/dev/null 2>&1; then gosec -fmt=text -exclude-dir=vendor -exclude-dir=build -exclude=G115,G304,G301,G306,G703,G204,G117,G704 -severity=medium -confidence=medium ./...; else echo "gosec not installed. Run: go install github.com/securego/gosec/v2/cmd/gosec@latest"; exit 1; fi'
        language: system
        pass_filenames: false
        files: '\.go$'

  # Prettier
  - repo: https://github.com/pre-commit/mirrors-prettier
    rev: v4.0.0-alpha.8
    hooks:
      - id: prettier
        name: Prettier
        types_or: [markdown, yaml, json, xml]
        exclude: '^(\.claude/|\.ai-scratch/|\.secrets\.baseline)'
        additional_dependencies:
          - prettier@3.5.3
          - "@prettier/plugin-xml@3.4.1"

  # Markdown linting
  - repo: https://github.com/igorshubovych/markdownlint-cli
    rev: v0.43.0
    hooks:
      - id: markdownlint
        name: Markdown Lint
        args: ["--fix", "--disable", "MD013", "MD025", "MD003", "--"]
        files: '\.md$'
        exclude: '^(\.claude/|\.github/agents/)'

  # Shell script linting
  - repo: https://github.com/shellcheck-py/shellcheck-py
    rev: v0.10.0.1
    hooks:
      - id: shellcheck
        name: ShellCheck
        args: ["--severity=error"]

  # Detect secrets
  - repo: https://github.com/Yelp/detect-secrets
    rev: v1.5.0
    hooks:
      - id: detect-secrets
        name: Detect Secrets
        args: ["--baseline", ".secrets.baseline"]
        exclude: '^(go\.sum|.*\.lock)$'

  # Spell checking
  - repo: https://github.com/codespell-project/codespell
    rev: v2.3.0
    hooks:
      - id: codespell
        name: Code Spell Checker
        args:
          - --ignore-words-list=crate,nd,sav,fo,ue,numer,unexpect,aNULL,hax
          - --skip="*.sum,*.lock,*.json,*.mod,coverage.html"
          - --quiet-level=2

  # Go vulnerability check
  - repo: local
    hooks:
      - id: govulncheck
        name: Go Vulnerability Check
        entry: bash -c 'if command -v govulncheck >/dev/null 2>&1; then govulncheck ./...; else echo "govulncheck not installed. Run: go install golang.org/x/vuln/cmd/govulncheck@latest"; exit 1; fi'
        language: system
        pass_filenames: false
        files: '(\.go$|go\.mod$)'

  # Go mod verify
  - repo: local
    hooks:
      - id: go-mod-verify
        name: Go Mod Verify
        entry: go mod verify
        language: system
        pass_filenames: false
        files: '^go\.(mod|sum)$'

  # Check VERSION file format
  - repo: local
    hooks:
      - id: check-version-format
        name: Check VERSION Format
        entry: bash -c 'if [[ -f VERSION ]]; then version=$(cat VERSION); if ! [[ $version =~ ^[0-9]{4}\.[0-9]+\.[0-9]+$ ]]; then echo "VERSION file must follow YYYY.M.D format (found $version)"; exit 1; fi; fi'
        language: system
        pass_filenames: false
        files: "^VERSION$"
```

**Step 2: Run pre-commit to verify**

Run: `pre-commit run --all-files`
Expected: Most hooks pass. Fix any issues iteratively.

**Step 3: Commit**

```bash
git add .pre-commit-config.yaml
git commit -m "feat: add pre-commit configuration with Go, security, and formatting hooks"
```

---

### Task 3: Update Makefile

**Files:**

- Modify: `Makefile`

**Step 1: Add pre-commit targets to Makefile**

Add to `.PHONY`:

```makefile
.PHONY: all build build-local test test-short test-coverage clean lint security-check package deploy pre-commit-install pre-commit-run deps
```

Add targets after the `deploy` target:

```makefile
pre-commit-install:
 @echo "Installing pre-commit hooks..."
 pre-commit install
 pre-commit install --hook-type commit-msg

pre-commit-run:
 pre-commit run --all-files
```

Also update `lint` target to use the config file:

```makefile
lint:
 golangci-lint run --config .golangci.yml --max-issues-per-linter 0 --max-same-issues 0 ./...
```

And update `security-check` to include gosec:

```makefile
security-check:
 gosec -fmt=text -exclude-dir=vendor -exclude-dir=build -exclude=G115,G304,G301,G306,G703,G204,G117,G704 -severity=medium -confidence=medium ./...
 govulncheck ./...
 go mod verify
```

**Step 2: Verify targets work**

Run: `make lint && make security-check`

**Step 3: Commit**

```bash
git add Makefile
git commit -m "feat: add pre-commit Makefile targets, update lint and security-check"
```

---

### Task 4: Create AGENTS.md

**Files:**

- Create: `AGENTS.md`

**Step 1: Write AGENTS.md**

Content adapted from reference — see design doc section 3. Includes:

- Project identity table
- Project structure
- Architecture diagram (layered)
- Key interfaces (storage.Adapter, engine.Handler) with method signatures
- Build commands
- Code style and conventions
- Core patterns (factory, build tags, WAL mode, WebSocket hub)
- Step-by-step guides for adding storage adapter, API endpoint, engine handler
- Anti-patterns
- Testing conventions
- Key dependencies table

**Step 2: Commit**

```bash
git add AGENTS.md
git commit -m "feat: add AGENTS.md as single source of truth for AI assistants"
```

---

### Task 5: Create .github/copilot-instructions.md

**Files:**

- Create: `.github/copilot-instructions.md`

**Step 1: Write copilot-instructions.md**

````markdown
# Copilot Instructions

> **Read [`../AGENTS.md`](../AGENTS.md) first** — it is the single source of truth for this project.

Go-based Unraid backup/restore plugin with REST API and WebSocket. **Language:** Go 1.26, **Target:** Linux/amd64 (Unraid OS). Third-party community plugin.

## Copilot Workflow

- Follow Go best practices: idiomatic style, `fmt.Errorf("context: %w", err)`, context propagation
- Code must pass `golangci-lint` and `go vet`
- Run `make pre-commit-run` before committing
- Follow **Conventional Commits**: `feat(scope):`, `fix(scope):`, `docs(scope):`

## Path-Specific Instructions

| File                            | Applies To                 |
| ------------------------------- | -------------------------- |
| `go.instructions.md`            | `**/*.go`                  |
| `engine.instructions.md`        | `internal/engine/**/*.go`  |
| `api-handlers.instructions.md`  | `internal/api/**/*.go`     |
| `storage.instructions.md`       | `internal/storage/**/*.go` |
| `db.instructions.md`            | `internal/db/**/*.go`      |
| `tests.instructions.md`         | `**/*_test.go`             |
| `yaml-markdown.instructions.md` | `**/*.{yaml,yml,md}`       |

## Reusable Prompts

- `Add Storage Adapter.prompt.md` — Adding a new storage backend
- `Add API Endpoint.prompt.md` — Adding a REST API endpoint
- `Add Engine Handler.prompt.md` — Adding a backup engine handler
- `Add Scheduler Job Type.prompt.md` — Adding a new job type
- `Debug Backup Issue.prompt.md` — Debugging backup/restore failures

## Quick Commands

```bash
make deps && make build-local  # Setup and build
make test                      # Run all tests
make pre-commit-run            # Lint + security checks
make deploy                    # Deploy to Unraid (Ansible)
```
````

````

**Step 2: Commit**

```bash
git add .github/copilot-instructions.md
git commit -m "feat: add GitHub Copilot instructions"
````

---

### Task 6: Create .github/instructions/ Files

**Files:**

- Create: `.github/instructions/go.instructions.md`
- Create: `.github/instructions/engine.instructions.md`
- Create: `.github/instructions/api-handlers.instructions.md`
- Create: `.github/instructions/storage.instructions.md`
- Create: `.github/instructions/db.instructions.md`
- Create: `.github/instructions/tests.instructions.md`
- Create: `.github/instructions/yaml-markdown.instructions.md`

**Step 1: Create all 7 instruction files**

Each file follows the pattern: YAML frontmatter with `applyTo` glob, then concise instructions specific to that area of the codebase. Content adapted for Vault's architecture — Chi router (not gorilla/mux), storage.Adapter interface, engine.Handler interface, SQLite repos, build tags for libvirt.

**Step 2: Commit**

```bash
git add .github/instructions/
git commit -m "feat: add path-specific AI instruction files"
```

---

### Task 7: Create .github/prompts/ Files

**Files:**

- Create: `.github/prompts/Add Storage Adapter.prompt.md`
- Create: `.github/prompts/Add API Endpoint.prompt.md`
- Create: `.github/prompts/Add Engine Handler.prompt.md`
- Create: `.github/prompts/Add Scheduler Job Type.prompt.md`
- Create: `.github/prompts/Debug Backup Issue.prompt.md`

**Step 1: Create all 5 prompt files**

Each file has YAML frontmatter (description, tools), then numbered steps specific to Vault's architecture.

**Step 2: Commit**

```bash
git add .github/prompts/
git commit -m "feat: add reusable AI prompt files for common workflows"
```

---

### Task 8: Create Supporting Files (.cursorrules, .ai-scratch, update CLAUDE.md)

**Files:**

- Create: `.cursorrules`
- Create: `.ai-scratch/.gitkeep`
- Modify: `CLAUDE.md`

**Step 1: Create .cursorrules**

````
# Cursor Rules — Vault

Read `AGENTS.md` in the project root for comprehensive instructions.

## Key Points

- **Language:** Go 1.26 | **Target:** Linux/amd64 (Unraid OS)
- **Architecture:** Layered (CLI → API Server → Handlers → DB/Storage/Engine)
- **Storage:** Always implement storage.Adapter interface, register in factory.go
- **Engine:** Always implement engine.Handler interface, use build tags for platform-specific code
- **Build:** CGO_ENABLED=0, pure Go (modernc.org/sqlite)
- **Database:** SQLite WAL mode, repos in internal/db/
- **Router:** Chi v5 (not gorilla/mux)
- **Testing:** Table-driven tests, httptest for API handlers
- **Pre-commit:** Run `make pre-commit-run` before committing

## Commands

```bash
make test && make lint        # Test and lint
make pre-commit-run           # Full pre-commit checks
make deploy                   # Deploy to Unraid (Ansible)
````

````

**Step 2: Create .ai-scratch/.gitkeep**

Empty file.

**Step 3: Update CLAUDE.md to reference AGENTS.md**

Add at the top after the header:
```markdown
> **Read [`AGENTS.md`](AGENTS.md) first** — it is the single source of truth for this project.
````

**Step 4: Commit**

```bash
git add .cursorrules .ai-scratch/.gitkeep CLAUDE.md
git commit -m "feat: add .cursorrules, .ai-scratch, update CLAUDE.md to reference AGENTS.md"
```

---

### Task 9: Verify Everything Works

**Files:** None (verification only)

**Step 1: Run go test**

Run: `go test ./... -v`
Expected: All tests pass.

**Step 2: Run go vet**

Run: `go vet ./...`
Expected: No issues.

**Step 3: Run go build for amd64**

Run: `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /dev/null ./cmd/vault/`
Expected: Compiles successfully.

**Step 4: Run go mod tidy and verify clean**

Run: `go mod tidy && git diff go.mod go.sum`
Expected: No changes (already tidy).

**Step 5: Run golangci-lint**

Run: `golangci-lint run --config=.golangci.yml ./...`
Expected: No issues.

**Step 6: Run govulncheck**

Run: `govulncheck ./...`
Expected: No vulnerabilities (or documented known ones).

**Step 7: Run gosec**

Run: `gosec -fmt=text -exclude-dir=vendor -exclude-dir=build -exclude=G115,G304,G301,G306,G703,G204,G117,G704 -severity=medium -confidence=medium ./...`
Expected: No issues.

**Step 8: Run pre-commit on all files**

Run: `pre-commit run --all-files`
Expected: All hooks pass.

**Step 9: Fix any failures and re-run until clean**

Iterate on exclusions/config as needed. Do not disable checks — tune exclusions.

**Step 10: Final commit if any fixes were needed**

```bash
git add -A
git commit -m "fix: resolve pre-commit and lint issues"
```
