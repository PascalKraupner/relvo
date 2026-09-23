.PHONY: build test check demo
build:
	go build -o bin/relvo ./cmd/relvo
test:
	go test -race ./...
check:
	go vet ./...
demo:
	go run ./cmd/relvo --demo
