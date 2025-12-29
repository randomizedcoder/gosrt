COMMIT := $(shell if [ -d .git ]; then git rev-parse HEAD; else echo "unknown"; fi)
SHORTCOMMIT := $(shell echo $(COMMIT) | head -c 7)

all: build

## check: Run all static analysis checks (seq-audit, lint)
## This prevents unsafe sequence arithmetic patterns from being introduced
check: audit-seq
	@echo "✅ All static checks passed"

## test: Run all tests (includes static checks first)
test: check
	go test -race -coverprofile=/dev/null -covermode=atomic -v ./...

## test-quick: Run tests without static checks (for development)
test-quick:
	go test -race -coverprofile=/dev/null -covermode=atomic -v ./...

## test-flags: Run flags integration tests (bash script)
## Tests that CLI flags are correctly parsed and applied to config
test-flags: client server
	@./contrib/common/test_flags.sh

## audit-metrics: Verify all metrics are defined, used, and exported to Prometheus
## Uses AST analysis to find discrepancies between metrics.go, usage, and handler.go
audit-metrics:
	@go run tools/metrics-audit/main.go

## audit-tests: Analyze test files for table-driven conversion opportunities
## Uses AST analysis to identify patterns and suggest table structures
## Options: make audit-tests ARGS="-suggest" or ARGS="-file=congestion/live/send_test.go"
audit-tests:
	@go run ./tools/test-table-audit/... $(ARGS)

## audit-combinations: Analyze test case structs for combinatorial coverage
## Uses reflection/AST to discover struct fields and suggest combinations
## Example: make audit-combinations FILE=congestion/live/loss_recovery_table_test.go STRUCT=LossRecoveryTestCase
audit-combinations:
	@go build -o /tmp/test-combinatorial-gen ./tools/test-combinatorial-gen/...
	@/tmp/test-combinatorial-gen $(FILE) $(STRUCT)

## audit-corners: Check corner case coverage for a table-driven test file
## Verifies that defined corner cases are actually tested
## Example: make audit-corners FILE=congestion/live/loss_recovery_table_test.go
audit-corners:
	@go build -o /tmp/test-combinatorial-gen ./tools/test-combinatorial-gen/...
	@/tmp/test-combinatorial-gen -coverage $(FILE)

## audit-corners-all: Check corner case coverage for ALL table-driven test files
audit-corners-all:
	@go build -o /tmp/test-combinatorial-gen ./tools/test-combinatorial-gen/...
	@echo "═══════════════════════════════════════════════════════════════════"
	@echo "CORNER CASE COVERAGE AUDIT - ALL TABLE-DRIVEN TESTS"
	@echo "═══════════════════════════════════════════════════════════════════"
	@for f in congestion/live/*_table_test.go; do \
		echo ""; \
		echo "━━━ $$f ━━━"; \
		/tmp/test-combinatorial-gen -coverage "$$f" 2>/dev/null | grep -E "(SUMMARY|Total|Covered|Missing|Critical|✅|❌)" || echo "  (no corner cases defined for this struct)"; \
	done

## ═══════════════════════════════════════════════════════════════════════════
## UNIFIED TEST AUDIT TOOL (recommended)
## ═══════════════════════════════════════════════════════════════════════════

## audit: Full audit of test files (unified tool)
## Usage: make audit [FILE=path/to/test.go]
audit:
	@go run ./tools/test-audit/... audit $(if $(FILE),-file $(FILE),)

## audit-classify: Classify test struct fields vs production code
## Usage: make audit-classify FILE=congestion/live/loss_recovery_table_test.go
audit-classify:
ifndef FILE
	$(error FILE is required. Usage: make audit-classify FILE=congestion/live/loss_recovery_table_test.go)
endif
	@go run ./tools/test-audit/... -file $(FILE) classify

## audit-coverage: Check corner case coverage (unified tool)
## Usage: make audit-coverage FILE=congestion/live/loss_recovery_table_test.go
audit-coverage:
ifndef FILE
	$(error FILE is required. Usage: make audit-coverage FILE=congestion/live/loss_recovery_table_test.go)
endif
	@go run ./tools/test-audit/... -file $(FILE) coverage

## audit-suggest: Get table-driven structure suggestions
## Usage: make audit-suggest FILE=congestion/live/nak_consolidate_test.go
audit-suggest:
ifndef FILE
	$(error FILE is required. Usage: make audit-suggest FILE=congestion/live/nak_consolidate_test.go)
endif
	@go run ./tools/test-audit/... -file $(FILE) suggest

## audit-seq: AST-based analysis for unsafe sequence arithmetic patterns
## Detects int32(a-b), direct comparisons, raw arithmetic on sequence numbers
## Usage: make audit-seq [PATH=./congestion/live]
audit-seq:
	@go run ./tools/seq-audit/... $(if $(PATH),$(PATH),./congestion/live ./circular)

## audit-all: Run full audit on all congestion/live test files
audit-all:
	@go run ./tools/test-audit/... audit -dir congestion/live

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

## integration-testing: Build the integration testing tool
integration-testing:
	cd contrib/integration_testing && go build -o integration_testing .

## test-parallel: Run parallel comparison test (root, CONFIG=Parallel-Starlink-5Mbps)
## sudo make test-parallel CONFIG=Parallel-Starlink-5Mbps
## sudo make test-parallel CONFIG=Parallel-Starlink-5Mbps VERBOSE=1
## Add PROFILES=<type> to enable profiling with Baseline vs HighPerf comparison (uses debug builds):
## sudo PROFILES=cpu make test-parallel CONFIG=Parallel-Starlink-5Mbps
## sudo PROFILES=cpu,mutex make test-parallel CONFIG=Parallel-Starlink-5Mbps
## sudo PROFILES=all make test-parallel CONFIG=Parallel-Starlink-5Mbps
# When PROFILES is set, use debug builds (with symbols) for better profile output
test-parallel: integration-testing $(if $(PROFILES),client-debug server-debug client-generator-debug,client server client-generator)
	@echo "NOTE: Parallel tests require root privileges for network namespace creation"
	@cd contrib/integration_testing && PROFILES=$(PROFILES) ./integration_testing parallel-test $(CONFIG) $(if $(VERBOSE),--verbose,)

## test-parallel-all: Run all parallel comparison tests (requires root)
test-parallel-all: integration-testing client server client-generator
	@echo "NOTE: Parallel tests require root privileges for network namespace creation"
	@cd contrib/integration_testing && ./integration_testing parallel-test-all

## test-isolation-list: List available isolation test configurations
test-isolation-list:
	@cd contrib/integration_testing && go run . list-isolation-configs

## test-isolation: Run a single isolation test (root, CONFIG=Isolation-5M-CG-IoUrSend)
## sudo make test-isolation CONFIG=Isolation-5M-CG-IoUrSend
## Add PRINT_PROM=true to see all Prometheus metrics:
## sudo make test-isolation CONFIG=Isolation-5M-CG-IoUrSend PRINT_PROM=true
## sudo make test-isolation CONFIG=Isolation-5M-Server-NakBtree-IoUr PRINT_PROM=true
## Add PROFILES=<type> to enable profiling (generates HTML report, uses debug builds):
## sudo PROFILES=cpu make test-isolation CONFIG=Isolation-5M-Server-NakBtree-IoUr
## sudo PROFILES=cpu,mutex make test-isolation CONFIG=Isolation-5M-Server-NakBtree-IoUr
## sudo PROFILES=all make test-isolation CONFIG=Isolation-5M-Server-NakBtree-IoUr
## sudo PRINT_PROM=true make test-isolation CONFIG=Isolation-5M-EventLoop-NoIOUring
# When PROFILES is set, use debug builds (with symbols) for better profile output
test-isolation: $(if $(PROFILES),server-debug client-generator-debug,server client-generator)
	@echo "NOTE: Isolation tests require root privileges for network namespace creation"
	@cd contrib/integration_testing && PRINT_PROM=$(PRINT_PROM) PROFILES=$(PROFILES) go run . isolation-test $(CONFIG)

## test-isolation-all: Run all 7 isolation tests (~3.5 min, captures output to temp dir)
## sudo make test-isolation-all
test-isolation-all: server client-generator
	@echo "NOTE: Isolation tests require root privileges for network namespace creation"
	@chmod +x contrib/integration_testing/run_isolation_tests.sh
	@contrib/integration_testing/run_isolation_tests.sh

## test-isolation-strategies: Run all 6 ring retry strategy tests (~72s total)
## Compares Sleep, Next, Random, Adaptive, Spin, Hybrid strategies
## sudo make test-isolation-strategies
test-isolation-strategies: server client-generator
	@echo "=== Ring Retry Strategy Comparison Tests ==="
	@echo "Running 6 strategy tests (12s each = ~72s total)"
	@for strategy in Sleep Next Random Adaptive Spin Hybrid; do \
		echo ""; \
		echo "=== Testing Strategy: $$strategy ==="; \
		(cd contrib/integration_testing && go run . isolation-test Isolation-5M-Strategy-$$strategy); \
	done
	@echo ""
	@echo "=== Strategy Comparison Complete ==="
	@echo "Compare RTT (us) and Drops columns to find best strategy"

## test-matrix-list: List all matrix-generated tests (64 tests)
test-matrix-list:
	@cd contrib/integration_testing && go run . matrix-list

## test-matrix-summary: Show matrix test summary by tier and category
test-matrix-summary:
	@cd contrib/integration_testing && go run . matrix-summary

## test-matrix-tier1-list: List Tier 1 (Core) tests (~25 tests)
test-matrix-tier1-list:
	@cd contrib/integration_testing && go run . matrix-list-tier1

## test-matrix-tier2-list: List Tier 1+2 (Daily) tests (~42 tests)
test-matrix-tier2-list:
	@cd contrib/integration_testing && go run . matrix-list-tier2

## test-matrix-tier1: Run Tier 1 (Core) tests (require root, ~40 min)
## sudo make test-matrix-tier1
test-matrix-tier1: client server client-generator
	@echo "NOTE: Matrix tests require root privileges for network namespace creation"
	@cd contrib/integration_testing && go run . matrix-run-tier1

## test-matrix-tier2: Run Tier 1+2 (Daily) tests (require root, ~70 min)
## sudo make test-matrix-tier2
test-matrix-tier2: client server client-generator
	@echo "NOTE: Matrix tests require root privileges for network namespace creation"
	@cd contrib/integration_testing && go run . matrix-run-tier2

## test-matrix-all: Run all matrix tests (require root, ~100 min)
## sudo make test-matrix-all
test-matrix-all: client server client-generator
	@echo "NOTE: Matrix tests require root privileges for network namespace creation"
	@cd contrib/integration_testing && go run . matrix-run-all

## test-clean-matrix-list: List all clean network matrix tests (~42 tests)
test-clean-matrix-list:
	@cd contrib/integration_testing && go run . clean-matrix-list

## test-clean-matrix-summary: Show clean network test summary by tier
test-clean-matrix-summary:
	@cd contrib/integration_testing && go run . clean-matrix-summary

## test-clean-matrix-tier1-list: List Tier 1 clean network tests (~14 tests)
test-clean-matrix-tier1-list:
	@cd contrib/integration_testing && go run . clean-matrix-tier1-list

## test-clean-matrix-tier2-list: List Tier 1+2 clean network tests (~24 tests)
test-clean-matrix-tier2-list:
	@cd contrib/integration_testing && go run . clean-matrix-tier2-list

## test-clean-matrix-tier1: Run Tier 1 clean network tests (no root, ~4 min)
## make test-clean-matrix-tier1
test-clean-matrix-tier1: client server client-generator
	@cd contrib/integration_testing && go run . clean-matrix-run-tier1

## test-clean-matrix-tier2: Run Tier 1+2 clean network tests (no root, ~6 min)
## make test-clean-matrix-tier2
test-clean-matrix-tier2: client server client-generator
	@cd contrib/integration_testing && go run . clean-matrix-run-tier2

## test-clean-matrix-all: Run all clean network tests (no root, ~10 min)
## make test-clean-matrix-all
test-clean-matrix-all: client server client-generator
	@cd contrib/integration_testing && go run . clean-matrix-run-all

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

## ============================================================================
## Receiver Stream Tests (Table-Driven Unit Tests)
## ============================================================================
## These tests verify NAK generation, packet ordering, and wraparound handling
## across all receiver configurations (Original, NakBtree, NakBtreeF, NakBtreeFr)

## test-stream-tier1: Run Tier 1 stream tests (~50 tests, <3s) - every PR
## These are the core validation tests that should pass for every change.
test-stream-tier1:
	@echo "=== Tier 1 Stream Tests (Core Validation) ==="
	go test -v ./congestion/live -run 'TestStream_Tier1'

## test-stream-tier2: Run Tier 2 stream tests (~200 tests, <15s) - daily CI
## Extended coverage including wraparound and reordering scenarios.
test-stream-tier2:
	@echo "=== Tier 2 Stream Tests (Extended Coverage) ==="
	go test -v ./congestion/live -run 'TestStream_Tier2'

## test-stream-tier3: Run Tier 3 stream tests (~1080 tests, <60s) - nightly CI
## Comprehensive coverage with all combinations of configs, loss, reordering.
test-stream-tier3:
	@echo "=== Tier 3 Stream Tests (Comprehensive) ==="
	go test -v ./congestion/live -run 'TestStream_Tier3'

## test-stream-all: Run all stream tier tests (~1330 tests)
test-stream-all:
	@echo "=== All Stream Tests (Tier 1 + 2 + 3) ==="
	go test -v ./congestion/live -run 'TestStream_Tier'

## test-stream-race: Run race detection on stream tests (Tier 1 only for speed)
test-stream-race:
	@echo "=== Stream Tests with Race Detection ==="
	go test -race -v ./congestion/live -run 'TestStream_Tier1'

## test-race: Run all receiver race detection tests
## These tests exercise concurrent Push/Tick/NAK operations.
test-race:
	@echo "=== Receiver Race Detection Tests ==="
	go test -race -v ./congestion/live -run 'TestRace'

## test-race-wraparound: Run race test specifically for sequence wraparound
test-race-wraparound:
	@echo "=== Wraparound Race Detection Test ==="
	go test -race -v ./congestion/live -run 'TestRace_SequenceWraparound'

## test-race-eventloop: Run EventLoop race tests (real goroutines + tickers)
## These are particularly valuable because EventLoop runs with real concurrency.
test-race-eventloop:
	@echo "=== EventLoop Race Detection Tests ==="
	go test -race -v ./congestion/live -run 'TestRace_EventLoop' -timeout 60s

## bench-receiver: Run receiver benchmarks (config comparison)
bench-receiver:
	@echo "=== Receiver Configuration Benchmarks ==="
	go test -bench='BenchmarkConfigComparison|BenchmarkPush|BenchmarkTick' -benchmem ./congestion/live -run='^$$'

## bench-receiver-realistic: Run realistic receiver benchmarks (10-20Mbps, 3-30s streams)
bench-receiver-realistic:
	@echo "=== Realistic Receiver Benchmarks (10-20Mbps streams) ==="
	go test -bench='BenchmarkRealistic' -benchmem ./congestion/live -run='^$$'

## bench-receiver-full: Run all receiver benchmarks
bench-receiver-full:
	@echo "=== Full Receiver Benchmark Suite ==="
	go test -bench='.' -benchmem ./congestion/live -run='^$$'

## bench-nak-btree: Run NAK btree specific benchmarks
bench-nak-btree:
	@echo "=== NAK Btree Benchmarks ==="
	go test -bench='BenchmarkNakBtree|BenchmarkNakScan|BenchmarkConsolidate' -benchmem ./congestion/live -run='^$$'

## bench-seqless: Run SeqLess comparison benchmarks (fixed vs broken vs Number.Lt)
bench-seqless:
	@echo "=== SeqLess Comparison Benchmarks ==="
	go test -bench='BenchmarkComparison|BenchmarkSeqLess' -benchmem ./circular -run='^$$'

## test-circular: Run all circular package tests (including wraparound regression tests)
test-circular:
	@echo "=== Circular Package Tests (including SeqLess wraparound fix) ==="
	go test -v ./circular

## test-packet-store: Run packet store tests (including wraparound tests)
test-packet-store:
	@echo "=== Packet Store Tests (including wraparound) ==="
	go test -v ./congestion/live -run 'TestPacketStore'

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

## client-debug: Build client binary with debug symbols and no inlining (for profiling)
client-debug:
	cd contrib/client && CGO_ENABLED=0 go build -o client-debug -gcflags="all=-N -l" -a

client-all: client client-debug

## server: Build import binary
server:
	cd contrib/server && CGO_ENABLED=0 go build -o server -ldflags="-s -w" -a

## server-debug: Build server binary with debug symbols and no inlining (for profiling)
server-debug:
	cd contrib/server && CGO_ENABLED=0 go build -o server-debug -gcflags="all=-N -l" -a

## client-generator: Build client-generator binary
client-generator:
	cd contrib/client-generator && CGO_ENABLED=0 go build -o client-generator -ldflags="-s -w" -a

## client-generator-debug: Build client-generator binary with debug symbols and no inlining (for profiling)
client-generator-debug:
	cd contrib/client-generator && CGO_ENABLED=0 go build -o client-generator-debug -gcflags="all=-N -l" -a

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
# Receiver stream testing targets (table-driven unit tests)
.PHONY: test-stream-tier1 test-stream-tier2 test-stream-tier3 test-stream-all test-stream-race
.PHONY: test-race test-race-wraparound test-race-eventloop test-circular test-packet-store
# Network impairment testing targets (require root)
.PHONY: test-network-list test-network test-network-all test-network-quick network-setup network-cleanup network-status
# Parallel comparison testing targets (require root)
.PHONY: test-parallel-list test-parallel test-parallel-all integration-testing
# Isolation testing targets (require root)
.PHONY: test-isolation-list test-isolation test-isolation-all test-isolation-strategies
# Matrix-generated testing targets (parallel, require root)
.PHONY: test-matrix-list test-matrix-summary test-matrix-tier1-list test-matrix-tier2-list
.PHONY: test-matrix-tier1 test-matrix-tier2 test-matrix-all
# Matrix-generated clean network testing targets (no root needed)
.PHONY: test-clean-matrix-list test-clean-matrix-summary
.PHONY: test-clean-matrix-tier1-list test-clean-matrix-tier2-list
.PHONY: test-clean-matrix-tier1 test-clean-matrix-tier2 test-clean-matrix-all
# Benchmark targets
.PHONY: bench-packet bench-packet-all bench-packet-pool bench-circular
.PHONY: bench-receiver bench-receiver-realistic bench-receiver-full bench-nak-btree bench-seqless
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
