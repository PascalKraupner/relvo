set positional-arguments

air_version := 'v1.67.4'
air_dir := justfile_directory() / 'bin/tools' / air_version

# Show available commands.
default:
    @just --list

# Hot reload the demo, or pass connection flags instead. Installs Air locally on first use.
dev *args='--demo': _air
    #!/usr/bin/env bash
    set -euo pipefail
    # Air joins arguments into a shell command; quote each value for that second shell.
    quoted=()
    for arg in "$@"; do
        quoted+=("'${arg//\'/\'\\\'\'}'")
    done
    exec "{{air_dir}}/air" -c .air.toml -- "${quoted[@]}"

# Build the binary at bin/relvo.
build:
    go build -trimpath -o bin/relvo ./cmd/relvo

# Build and run Relvo. Arguments are forwarded unchanged.
run *args: build
    @./bin/relvo "$@"

# Build and explore the seeded demo without a database.
demo *args: build
    @./bin/relvo --demo "$@"

# Run race-enabled tests; optional Go test flags are forwarded.
test *args:
    go test -race "$@" ./...

# Run static analysis.
vet:
    go vet ./...

# Format Go source and tests.
fmt:
    gofmt -w cmd internal scripts/dev

# Check formatting without changing files.
fmt-check:
    @files="$(gofmt -l cmd internal scripts/dev)"; if [ -n "$files" ]; then printf 'Run just fmt to format:\n%s\n' "$files"; exit 1; fi

# Run formatting checks, static analysis, tests, and a build.
check: fmt-check vet test build

# Install the pinned Air version into this project's ignored bin directory.
tools:
    mkdir -p "{{air_dir}}"
    GOBIN="{{air_dir}}" go install github.com/air-verse/air@{{air_version}}

[private]
_air:
    @test -x "{{air_dir}}/air" || just tools
