PKG := github.com/lightninglabs/lndclient

LINT_PKG := github.com/golangci/golangci-lint/cmd/golangci-lint

GO_BIN := ${GOPATH}/bin
LINT_BIN := $(GO_BIN)/golangci-lint

LINT_COMMIT := v1.18.0

DEPGET := cd /tmp && GO111MODULE=on go get -v
GOBUILD := GO111MODULE=on go build -v
GOTEST := GO111MODULE=on go test -v

LINT = $(LINT_BIN) run -v
GOFILES = $(shell find . -type f -name '*.go')

GREEN := "\\033[0;32m"
NC := "\\033[0m"
define print
	echo $(GREEN)$1$(NC)
endef

default: build

# ============
# DEPENDENCIES
# ============
$(LINT_BIN):
	@$(call print, "Fetching linter")
	$(DEPGET) $(LINT_PKG)@$(LINT_COMMIT)

# ============
# INSTALLATION
# ============

build:
	@$(call print, "Building lndclient.")
	$(GOBUILD) $(PKG)

# =======
# TESTING
# =======

unit:
	@$(call print, "Running unit tests.")
	$(GOTEST) ./...

unit-race:
	@$(call print, "Running unit race tests.")
	env CGO_ENABLED=1 GORACE="history_size=7 halt_on_errors=1" $(GOTEST) ./...

# =========
# UTILITIES
# =========

fmt:
	@$(call print, "Formatting source.")
	gofmt -l -w -s $(GOFILES)

lint: $(LINT_BIN)
	@$(call print, "Linting source.")
	$(LINT)

.PHONY: default \
	build \
	unit \
	unit-race \
	fmt \
	lint
