PKG := github.com/lightninglabs/lndclient

TOOLS_DIR := tools

GOIMPORTS_PKG := github.com/rinchsan/gosimports/cmd/gosimports

GO_BIN := ${GOPATH}/bin
GOIMPORTS_BIN := $(GO_BIN)/gosimports

GOBUILD := go build -v
GOINSTALL := go install -v
GOTEST := go test -v

GOFILES_NOVENDOR = $(shell find . -type f -name '*.go' -not -path "./vendor/*")
GOLIST := go list -deps $(PKG)/... | grep '$(PKG)'| grep -v '/vendor/'

COMMIT := $(shell git describe --abbrev=40 --dirty)
LDFLAGS := -X $(PKG).Commit=$(COMMIT)

RM := rm -f
CP := cp
MAKE := make
XARGS := xargs -L 1

# Linting uses a lot of memory, so keep it under control by limiting the number
# of workers if requested.
ifneq ($(workers),)
LINT_WORKERS = --concurrency=$(workers)
endif

DOCKER_TOOLS = docker run -v $$(pwd):/build lndclient-tools

GREEN := "\\033[0;32m"
NC := "\\033[0m"
define print
	echo $(GREEN)$1$(NC)
endef

default: build

all: build check install

# ============
# DEPENDENCIES
# ============
$(GOIMPORTS_BIN):
	@$(call print, "Installing goimports.")
	cd $(TOOLS_DIR); go install -trimpath $(GOIMPORTS_PKG)

# ============
# INSTALLATION
# ============

build:
	@$(call print, "Building lndclient.")
	$(GOBUILD) -ldflags="$(LDFLAGS)" $(PKG)

docker-tools:
	@$(call print, "Building tools docker image.")
	docker build -q -t lndclient-tools $(TOOLS_DIR)

# =======
# TESTING
# =======

check: unit

unit:
	@$(call print, "Running unit tests.")
	$(GOTEST) ./...

unit-race:
	@$(call print, "Running unit race tests.")
	env CGO_ENABLED=1 GORACE="history_size=7 halt_on_errors=1" $(GOTEST) -race ./...

# =========
# UTILITIES
# =========
fmt: $(GOIMPORTS_BIN)
	@$(call print, "Fixing imports.")
	gosimports -w $(GOFILES_NOVENDOR)
	@$(call print, "Formatting source.")
	gofmt -l -w -s $(GOFILES_NOVENDOR)

lint: docker-tools
	@$(call print, "Linting source.")
	$(DOCKER_TOOLS) golangci-lint run -v $(LINT_WORKERS)

.PHONY: default \
	build \
	unit \
	unit-race \
	fmt \
	lint
