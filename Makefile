.PHONY: gen build test lint install

# Two-party ECDSA (ecdsa2p) is Coinbase's cb-mpc, C++ behind cgo, through the
# mishmar-io fork of cb-mpc-go checked out next to this repo (CBMPC_DIR). Its
# scripts/go_with_cpp.sh builds the C++ library once and sets the cgo flags,
# so every Go command here goes through it (the git hooks included: a bare
# `go test` cannot find the C++ headers).
CBMPC_DIR ?= ../cb-mpc-go
# The static OpenSSL 3.6.1 cb-mpc links against: the workspace copy next to the repos when there is
# one (the dev machines), else where the fork's scripts/openssl install it (CI, the Dockerfile).
CBMPC_OPENSSL_ROOT ?= $(if $(wildcard $(CBMPC_DIR)/../.mishmar-dev/openssl-3.6.1/lib),$(abspath $(CBMPC_DIR)/../.mishmar-dev/openssl-3.6.1),/usr/local/opt/openssl@3.6.1)
# cb-mpc prints a stack trace on every protocol failure unless built with DY_NO_LOG; a refused
# message is an expected failure. The toolchain line in go.mod is newer than most installed Gos.
CXXFLAGS ?= -DDY_NO_LOG
GOTOOLCHAIN ?= auto
export CBMPC_OPENSSL_ROOT CXXFLAGS GOTOOLCHAIN
GO = bash $(CBMPC_DIR)/scripts/go_with_cpp.sh go

# The contract's Go types (api/types.gen.go) are generated from spec/openapi.yaml.
gen: ## Regenerate api/types.gen.go from the contract and signerapi from the signer service's spec
	go generate ./api
	go generate ./signerapi

build:
	$(GO) build ./...

# The dev binaries live next to the repos (.mishmar-dev/bin is on PATH once env.sh is sourced).
BIN ?= ../.mishmar-dev/bin

install: ## Build the mishmar binary (CLI, agent signer, signer service) into BIN
	mkdir -p $(BIN) && $(GO) build -o $(BIN)/mishmar ./cmd/mishmar

test:
	$(GO) test ./...

lint:
	$(GO) vet ./...
	@test -z "$$(gofmt -l .)" || { echo "gofmt: files need formatting:"; gofmt -l .; exit 1; }
