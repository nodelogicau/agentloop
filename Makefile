.PHONY: all test lint fmt vet tidy

all: test

# Unit tests only: no test calls a model.
test:
	go test ./... -count=1 -race

fmt:
	gofmt -l -w .

vet:
	go vet ./...

lint: vet
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run ./... || echo "golangci-lint not installed; ran go vet only"

tidy:
	go mod tidy
