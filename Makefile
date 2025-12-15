COMMIT := $(shell if [ -d .git ]; then git rev-parse HEAD; else echo "unknown"; fi)
SHORTCOMMIT := $(shell echo $(COMMIT) | head -c 7)

all: build

## test: Run all tests
test:
	go test -race -coverprofile=/dev/null -covermode=atomic -v ./...

## test-flags: Run flags integration tests (bash script)
## Tests that CLI flags are correctly parsed and applied to config
test-flags: client server
	@./contrib/common/test_flags.sh

## audit-metrics: Verify all metrics are defined, used, and exported to Prometheus
## Uses AST analysis to find discrepancies between metrics.go, usage, and handler.go
audit-metrics:
	@go run tools/metrics-audit/main.go

## test-integration: Run integration tests (context cancellation, graceful shutdown, etc.)
test-integration: client server client-generator
	@cd contrib/integration_testing && go run . graceful-shutdown-sigint

## test-integration-all: Run integration tests with all configurations
test-integration-all: client server client-generator
	@cd contrib/integration_testing && go run . graceful-shutdown-sigint-all

## test-integration-config: Run integration test with specific configuration (use CONFIG=name)
test-integration-config: client server client-generator
	@cd contrib/integration_testing && go run . graceful-shutdown-sigint-config $(CONFIG)

## test-integration-list: List available integration test configurations
## make test-integration-list
test-integration-list:
	@cd contrib/integration_testing && go run . list-configs

## test-network-list: List available network impairment test configurations
## make test-network-list
test-network-list:
	@cd contrib/integration_testing && go run . list-network-configs

## test-network: Run network impairment test (root, CONFIG=Network-Loss2pct-5Mbps, VERBOSE=1 for detailed metrics)
## sudo make test-network CONFIG=Network-Starlink-5Mbps
## sudo make test-network CONFIG=Network-Starlink-5Mbps VERBOSE=1 SRT_NETWORK_DEBUG=1
test-network: client server client-generator
	@echo "NOTE: Network impairment tests require root privileges for network namespace creation"
	@cd contrib/integration_testing && go run . network-test $(CONFIG) $(if $(VERBOSE),--verbose,)

## test-network-all: Run all network impairment tests (requires root)
test-network-all: client server client-generator
	@echo "NOTE: Network impairment tests require root privileges for network namespace creation"
	@cd contrib/integration_testing && go run . network-test-all

## test-network-quick: Run quick network tests (2% and 5% loss only, requires root)
test-network-quick: client server client-generator
	@echo "NOTE: Network impairment tests require root privileges"
	@cd contrib/integration_testing && go run . network-test Network-Loss2pct-5Mbps
	@cd contrib/integration_testing && go run . network-test Network-Loss5pct-5Mbps

## test-parallel-list: List available parallel comparison test configurations
test-parallel-list:
	@cd contrib/integration_testing && go run . list-parallel-configs

## test-parallel: Run parallel comparison test (root, CONFIG=Parallel-Starlink-5Mbps)
## sudo make test-parallel CONFIG=Parallel-Starlink-5Mbps
## sudo make test-parallel CONFIG=Parallel-Starlink-5Mbps VERBOSE=1
test-parallel: client server client-generator
	@echo "NOTE: Parallel tests require root privileges for network namespace creation"
	@cd contrib/integration_testing && go run . parallel-test $(CONFIG) $(if $(VERBOSE),--verbose,)

## test-parallel-all: Run all parallel comparison tests (requires root)
test-parallel-all: client server client-generator
	@echo "NOTE: Parallel tests require root privileges for network namespace creation"
	@cd contrib/integration_testing && go run . parallel-test-all

## test-isolation-list: List available isolation test configurations
test-isolation-list:
	@cd contrib/integration_testing && go run . list-isolation-configs

## test-isolation: Run a single isolation test (root, CONFIG=Isolation-CG-IoUringSend)
## sudo make test-isolation CONFIG=Isolation-CG-IoUringSend
## Add PRINT_PROM=true to see all Prometheus metrics:
## sudo make test-isolation CONFIG=Isolation-CG-IoUringSend PRINT_PROM=true
## sudo make test-isolation CONFIG=Isolation-Server-NakBtree-IoUringRecv PRINT_PROM=true
test-isolation: server client-generator
	@echo "NOTE: Isolation tests require root privileges for network namespace creation"
	@cd contrib/integration_testing && PRINT_PROM=$(PRINT_PROM) go run . isolation-test $(CONFIG)

## test-isolation-all: Run all 7 isolation tests (~3.5 min, captures output to temp dir)
## sudo make test-isolation-all
test-isolation-all: server client-generator
	@echo "NOTE: Isolation tests require root privileges for network namespace creation"
	@chmod +x contrib/integration_testing/run_isolation_tests.sh
	@contrib/integration_testing/run_isolation_tests.sh

## test-shutdown: Quick test for graceful shutdown of each component (no root needed)
test-shutdown: client server client-generator
	@cd contrib/integration_testing && ./test_shutdown.sh $(TEST)

## network-setup: Set up network namespaces for manual testing (requires root)
network-setup:
	@echo "Setting up network namespaces..."
	@cd contrib/integration_testing/network && sudo ./setup.sh

## network-cleanup: Clean up network namespaces (requires root)
network-cleanup:
	@echo "Cleaning up network namespaces..."
	@cd contrib/integration_testing/network && sudo ./cleanup.sh

## network-status: Show current network namespace status
network-status:
	@cd contrib/integration_testing/network && sudo ./status.sh

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

## client-generator: Build client-generator binary
client-generator:
	cd contrib/client-generator && CGO_ENABLED=0 go build -o client-generator -ldflags="-s -w" -a

## client-generator-debug: Build client-generator binary with debug symbols
client-generator-debug:
	cd contrib/client-generator && CGO_ENABLED=0 go build -o client-generator-debug -a

server-profile:
	go tool pprof -http=0.0.0.0:8080 ./contrib/server/server-debug cpu.pprof

server-all: server server-debug

## clean: Remove all built binaries (forces rebuild on next test)
clean:
	@echo "Removing built binaries..."
	rm -f contrib/server/server contrib/server/server-debug
	rm -f contrib/client/client contrib/client/client-debug
	rm -f contrib/client-generator/client-generator contrib/client-generator/client-generator-debug
	@echo "Clean complete. Run 'make client server client-generator' to rebuild."

## clean-all: Remove all built binaries and generated files
clean-all: clean
	rm -f cover.out cover.html
	rm -f cpu.pprof mem.pprof

## rebuild: Clean and rebuild all binaries
rebuild: clean client server client-generator

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
.PHONY: test test-flags test-flags-integration test-integration test-integration-all test-integration-config test-integration-list test-congestion-live test-packet-pool test-packet fuzz coverage
# Network impairment testing targets (require root)
.PHONY: test-network-list test-network test-network-all test-network-quick network-setup network-cleanup network-status
# Parallel comparison testing targets (require root)
.PHONY: test-parallel-list test-parallel test-parallel-all
# Isolation testing targets (require root)
.PHONY: test-isolation-list test-isolation test-isolation-all
# Benchmark targets
.PHONY: bench-packet bench-packet-all bench-packet-pool bench-circular
# Code quality targets
.PHONY: vet fmt lint
# Dependency management targets
.PHONY: update tidy vendor
# Build targets
.PHONY: client client-debug client-generator client-generator-debug server server-debug
# Clean targets
.PHONY: clean clean-all rebuild
# Other targets
.PHONY: commit docker logtopics help

## help: Show all commands
help: Makefile
	@echo
	@echo " Choose a command:"
	@echo
	@sed -n 's/^##//p' $< | column -t -s ':' |  sed -e 's/^/ /'
	@echo
