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

# Check Python
echo "Checking dependencies..."
if ! command_exists python3; then
    echo -e "${YELLOW}Python 3 not found, installing...${NC}"
    if [[ "$OSTYPE" == "darwin"* ]]; then
        brew install python3 || { echo -e "${RED}Failed to install Python 3${NC}"; exit 1; }
    else
        sudo apt-get update && sudo apt-get install -y python3 python3-pip || { echo -e "${RED}Failed to install Python 3${NC}"; exit 1; }
    fi
fi
echo -e "${GREEN}✓ Python 3 found${NC}"

# Check pip
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
    "golang.org/x/tools/cmd/goimports@latest"
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

# Check Node.js (needed for prettier and markdownlint)
echo ""
echo "Checking Node.js..."
if ! command_exists node; then
    echo -e "${YELLOW}Node.js not found, installing...${NC}"
    if [[ "$OSTYPE" == "darwin"* ]]; then
        brew install node || echo -e "${YELLOW}Failed to install Node.js. Prettier/markdownlint hooks will use pre-commit's built-in node.${NC}"
    else
        echo -e "${YELLOW}Install Node.js manually for prettier/markdownlint support${NC}"
    fi
else
    echo -e "${GREEN}✓ Node.js found${NC}"
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
