.PHONY: gen build test lint

# The contract's Go types (api/types.gen.go) are generated from spec/openapi.yaml.
gen: ## Regenerate api/types.gen.go from the contract
	go generate ./api

build:
	go build ./...

test:
	go test ./...

lint:
	go vet ./...
	@test -z "$$(gofmt -l .)" || { echo "gofmt: files need formatting:"; gofmt -l .; exit 1; }
