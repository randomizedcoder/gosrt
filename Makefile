COMMIT := $(shell if [ -d .git ]; then git rev-parse HEAD; else echo "unknown"; fi)
SHORTCOMMIT := $(shell echo $(COMMIT) | head -c 7)

all: build

## test: Run all tests
test:
	go test -race -coverprofile=/dev/null -covermode=atomic -v ./...

## test-flags: Run flags tests (Go unit tests)
test-flags:
	CGO_ENABLED=0 go test -v ./contrib/common/

## test-flags-integration: Run flags integration tests (bash script)
test-flags-integration: client server
	@./contrib/common/test_flags.sh

test-congestion-live:
	go test -v ./congestion/live

## test-packet-pool: Run packet pooling tests
test-packet-pool:
	go test -v ./packet -run TestPacketPool

## test-packet: Run all packet tests
test-packet:
	go test -v ./packet

## bench-packet: Run packet benchmarks
bench-packet:
	go test -bench=BenchmarkNewPacket -benchmem ./packet

## bench-packet-all: Run all packet benchmarks
bench-packet-all:
	go test -bench=. -benchmem ./packet

## bench-packet-pool: Run packet pooling benchmarks (with comparison)
bench-packet-pool:
	@echo "=== Packet Pooling Benchmarks ==="
	go test -bench=BenchmarkNewPacket -benchmem -count=5 ./packet | tee /tmp/bench-packet-pool.txt

## bench-circular: Benchmark circular number comparison functions (Lt vs LtBranchless)
bench-circular:
	@echo "=== Circular Number Comparison Benchmarks ==="
	go test -bench=BenchmarkLt -benchmem -benchtime=2s ./circular | tee /tmp/bench-circular.txt
	@echo ""
	@echo "Results saved to /tmp/bench-packet-pool.txt"

## fuzz: Run fuzz tests
fuzz:
	go test -fuzz=Fuzz -run=^Fuzz ./packet -fuzztime 30s

## vet: Analyze code for potential errors
vet:
	go vet ./...

## fmt: Format code
fmt:
	go fmt ./...

## update: Update dependencies
update:
	go get -u -t
	@-$(MAKE) tidy
	@-$(MAKE) vendor

## tidy: Tidy up go.mod
tidy:
	go mod tidy

## vendor: Update vendored packages
vendor:
	go mod vendor

## lint: Static analysis with staticcheck
lint:
	staticcheck ./...

## client: Build import binary
client:
	cd contrib/client && CGO_ENABLED=0 go build -o client -ldflags="-s -w" -a

## client-debug: Build client binary with debug symbols
client-debug:
	cd contrib/client && CGO_ENABLED=0 go build -o client-debug -a

client-all: client client-debug

## server: Build import binary
server:
	cd contrib/server && CGO_ENABLED=0 go build -o server -ldflags="-s -w" -a

## server-debug: Build server binary with debug symbols
server-debug:
	cd contrib/server && CGO_ENABLED=0 go build -o server-debug -a

server-profile:
	go tool pprof -http=0.0.0.0:8080 ./contrib/server/server-debug cpu.pprof

server-all: server server-debug

## coverage: Generate code coverage analysis
coverage:
	go test -race -coverprofile=cover.out -timeout 60s -v ./...
	go tool cover -html=cover.out -o cover.html

## commit: Prepare code for commit (vet, fmt, test)
commit: vet fmt lint test
	@echo "No errors found. Ready for a commit."

## docker: Build standard Docker image
docker:
	docker build -t gosrt:$(SHORTCOMMIT) .

## logtopics: Extract all logging topics
logtopics:
	grep -ERho 'log\("([^"]+)' *.go | sed -E -e 's/log\("//' | sort -u

## nix-shell: To resolve gcc linking when running go branchmark tests
nixshell:
	nix-shell -p gcc pkg-config zlib

# Testing targets
.PHONY: test test-flags test-flags-integration test-congestion-live test-packet-pool test-packet fuzz coverage
# Benchmark targets
.PHONY: bench-packet bench-packet-all bench-packet-pool bench-circular
# Code quality targets
.PHONY: vet fmt lint
# Dependency management targets
.PHONY: update tidy vendor
# Build targets
.PHONY: client client-debug server server-debug
# Other targets
.PHONY: commit docker logtopics help

## help: Show all commands
help: Makefile
	@echo
	@echo " Choose a command:"
	@echo
	@sed -n 's/^##//p' $< | column -t -s ':' |  sed -e 's/^/ /'
	@echo
