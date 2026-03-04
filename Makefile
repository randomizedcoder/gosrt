COMMIT := $(shell if [ -d .git ]; then git rev-parse HEAD; else echo "unknown"; fi)
SHORTCOMMIT := $(shell echo $(COMMIT) | head -c 7)

# ═══════════════════════════════════════════════════════════════════════════════
# EXPERIMENTAL GO FEATURES (Go 1.25+)
# ═══════════════════════════════════════════════════════════════════════════════
# These experiments are ENABLED BY DEFAULT for better performance.
# They are experimental in Go 1.25 but expected to become stable in future releases.
# When Go 1.26+ stabilizes these, this variable can be removed.
#
# jsonv2: New JSON implementation - encoding at parity, decoding substantially faster
#   See: https://go.dev/doc/go1.25#encoding-json-v2
#
# greenteagc: New garbage collector - 10-40% less GC overhead
#   Better locality and CPU scalability for small objects
#
# To disable: make build GOEXPERIMENT=
# ═══════════════════════════════════════════════════════════════════════════════
GOEXPERIMENT ?= jsonv2,greenteagc
export GOEXPERIMENT

.PHONY: all build check code-audit code-audit-seq code-audit-metrics code-audit-test test test-quick audit-metrics
.PHONY: coverage coverage-html coverage-check coverage-by-package

all: build

## build: Build all main binaries (client, server, client-generator)
build: client server client-generator

## check: Run all static analysis checks (code-audit, lint)
## This prevents unsafe patterns from being introduced
check: code-audit-seq
	@echo "✅ All static checks passed"

## code-audit: Unified comprehensive code quality audit
## Runs: sequence analysis, metrics verification, test coverage
## Usage: make code-audit
code-audit:
	@go run ./tools/code-audit/... all

## code-audit-seq: Sequence arithmetic analysis only (fast)
code-audit-seq:
	@go run ./tools/code-audit/... seq ./congestion/live ./circular

## code-audit-metrics: Prometheus metrics alignment check
code-audit-metrics:
	@go run ./tools/code-audit/... metrics

## code-audit-test: Test structure and coverage analysis
## Usage: make code-audit-test [FILE=path/to/test.go]
code-audit-test:
	@go run ./tools/code-audit/... test $(if $(FILE),-file $(FILE),) audit

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

## test-adaptive-backoff: Run adaptive backoff unit tests
## Tests the Yield/Sleep mode switching mechanism
test-adaptive-backoff:
	go test -v -timeout 60s -run TestAdaptiveBackoff ./congestion/live/send/

## test-adaptive-backoff-race: Run adaptive backoff tests with race detector
test-adaptive-backoff-race:
	go test -v -race -timeout 60s -run TestAdaptiveBackoff ./congestion/live/send/

## bench-adaptive-backoff: Run adaptive backoff benchmarks
bench-adaptive-backoff:
	go test -bench=BenchmarkAdaptiveBackoff -benchmem -timeout 60s ./congestion/live/send/

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
## sudo make test-parallel CONFIG=Parallel-Loss-L5-20M-Base-vs-FullSendEL-GEO CONFIGONLY=true
## Add CONFIGONLY=true to print CLI flags without running (no root required):
## make test-parallel CONFIG=Parallel-Starlink-5Mbps CONFIGONLY=true
## Add PROFILES=<type> to enable profiling with Baseline vs HighPerf comparison (uses debug builds):
## sudo PROFILES=cpu make test-parallel CONFIG=Parallel-Starlink-5Mbps
## sudo PROFILES=cpu,mutex make test-parallel CONFIG=Parallel-Starlink-5Mbps
## sudo PROFILES=all make test-parallel CONFIG=Parallel-Starlink-5Mbps
## Add TCPDUMP_* to capture packets for analysis with tshark/wireshark:
## sudo TCPDUMP_CG=/tmp/cg.pcap TCPDUMP_SERVER=/tmp/server.pcap TCPDUMP_CLIENT=/tmp/client.pcap make test-parallel ...
# When PROFILES is set, use debug builds (with symbols) for better profile output
# When CONFIGONLY is set, skip building binaries and just print config
test-parallel: integration-testing $(if $(CONFIGONLY),,$(if $(PROFILES),client-debug server-debug client-generator-debug,client server client-generator))
	@if [ -n "$(CONFIGONLY)" ]; then \
		cd contrib/integration_testing && ./integration_testing parallel-test-config $(CONFIG); \
	else \
		echo "NOTE: Parallel tests require root privileges for network namespace creation"; \
		cd contrib/integration_testing && PROFILES=$(PROFILES) ./integration_testing parallel-test $(CONFIG) $(if $(VERBOSE),--verbose,); \
	fi

## test-parallel-config: Print CLI flags for a parallel test without running (no root required)
## make test-parallel-config CONFIG=Parallel-Starlink-5Mbps
test-parallel-config: integration-testing
	@cd contrib/integration_testing && ./integration_testing parallel-test-config $(CONFIG)

## test-parallel-all: Run all parallel comparison tests (requires root)
test-parallel-all: integration-testing client server client-generator
	@echo "NOTE: Parallel tests require root privileges for network namespace creation"
	@cd contrib/integration_testing && ./integration_testing parallel-test-all

## ============================================================================
## LOCKLESS SENDER PARALLEL TESTS (Phase 5+)
## ============================================================================
## These targets test the new lockless sender implementation.
## See lockless_sender_design.md for expected metrics.

## test-parallel-sender: Run sender lockless test (clean network, 20 Mb/s)
## sudo make test-parallel-sender
test-parallel-sender: integration-testing client server client-generator
	@echo "Running Lockless Sender test: Baseline vs SendEL"
	@cd contrib/integration_testing && ./integration_testing parallel-test Parallel-Clean-20M-Base-vs-SendEL

## test-parallel-sender-full: Run full lockless test (receiver + sender EventLoop)
## sudo make test-parallel-sender-full
test-parallel-sender-full: integration-testing client server client-generator
	@echo "Running Full Lockless test: FullEL vs FullSendEL"
	@cd contrib/integration_testing && ./integration_testing parallel-test Parallel-Clean-20M-FullEL-vs-FullSendEL

## test-parallel-sender-high: Run high throughput sender test (50 Mb/s)
## sudo make test-parallel-sender-high
test-parallel-sender-high: integration-testing client server client-generator
	@echo "Running High Throughput Sender test: 50 Mb/s"
	@cd contrib/integration_testing && ./integration_testing parallel-test Parallel-Clean-50M-Base-vs-SendEL

## test-parallel-sender-loss: Run sender test with 5% loss
## sudo make test-parallel-sender-loss
test-parallel-sender-loss: integration-testing client server client-generator
	@echo "Running Sender Loss test: 5% loss at 20 Mb/s"
	@cd contrib/integration_testing && ./integration_testing parallel-test Parallel-Loss-L5-20M-Base-vs-SendEL

## test-parallel-sender-starlink: Run sender test with Starlink impairment
## sudo make test-parallel-sender-starlink
test-parallel-sender-starlink: integration-testing client server client-generator
	@echo "Running Sender Starlink test: 20 Mb/s with Starlink pattern"
	@cd contrib/integration_testing && ./integration_testing parallel-test Parallel-Starlink-20M-Base-vs-SendEL

## test-parallel-sender-all: Run all sender lockless tests
## sudo make test-parallel-sender-all
test-parallel-sender-all: integration-testing client server client-generator
	@echo "Running all Lockless Sender tests..."
	@echo ""
	@echo "=== Test 1/5: Clean Network 20 Mb/s ==="
	@cd contrib/integration_testing && ./integration_testing parallel-test Parallel-Clean-20M-Base-vs-SendEL
	@echo ""
	@echo "=== Test 2/5: Full Lockless (Receiver + Sender) ==="
	@cd contrib/integration_testing && ./integration_testing parallel-test Parallel-Clean-20M-FullEL-vs-FullSendEL
	@echo ""
	@echo "=== Test 3/5: High Throughput 50 Mb/s ==="
	@cd contrib/integration_testing && ./integration_testing parallel-test Parallel-Clean-50M-Base-vs-SendEL
	@echo ""
	@echo "=== Test 4/5: 5% Loss ==="
	@cd contrib/integration_testing && ./integration_testing parallel-test Parallel-Loss-L5-20M-Base-vs-SendEL
	@echo ""
	@echo "=== Test 5/5: Starlink Pattern ==="
	@cd contrib/integration_testing && ./integration_testing parallel-test Parallel-Starlink-20M-Base-vs-SendEL
	@echo ""
	@echo "All Lockless Sender tests complete!"

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
## Add TCPDUMP_* to capture packets for analysis with tshark/wireshark:
## sudo TCPDUMP_CG=/tmp/cg.pcap TCPDUMP_SERVER=/tmp/server.pcap make test-isolation CONFIG=...
## Environment variables for packet capture:
##   TCPDUMP_CG or TCPDUMP_PUBLISHER: Capture at publisher/client-generator
##   TCPDUMP_SERVER or TCPDUMP_S: Capture at server
##   TCPDUMP_CLIENT or TCPDUMP_SUBSCRIBER or TCPDUMP_C: Capture at subscriber/client
## Analyze captures with tshark:
##   tshark -r /tmp/cg.pcap -Y "srt" -T fields -e frame.time_relative -e srt.type -e srt.seqno
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

## test-isolation-sender-list: List all sender-specific isolation tests
test-isolation-sender-list:
	@echo "Sender-specific isolation tests:"
	@cd contrib/integration_testing && go run . list-isolation-configs | grep -E "Send|SendEL"

## test-isolation-sender-phases: Run sender isolation tests by phase (~6 min total)
## Tests each sender feature in isolation: Btree, Ring, ControlRing, EventLoop
## sudo make test-isolation-sender-phases
test-isolation-sender-phases: server client-generator
	@echo "=== Lockless Sender Phase Isolation Tests ==="
	@echo "Testing each sender feature in isolation on CG side..."
	@for test in CG-SendBtree CG-SendRing CG-SendControlRing CG-SendEventLoop; do \
		echo ""; \
		echo "=== Testing: Isolation-5M-$$test ==="; \
		(cd contrib/integration_testing && go run . isolation-test Isolation-5M-$$test); \
	done
	@echo ""
	@echo "=== Sender Phase Tests Complete ==="

## test-isolation-sender-server: Run sender isolation tests on server side (~4 tests, ~2 min)
## Tests sender features on the server (forwarding path)
## sudo make test-isolation-sender-server
test-isolation-sender-server: server client-generator
	@echo "=== Server-Side Sender Isolation Tests ==="
	@for test in Server-SendBtree Server-SendRing Server-SendControlRing Server-SendEventLoop; do \
		echo ""; \
		echo "=== Testing: Isolation-5M-$$test ==="; \
		(cd contrib/integration_testing && go run . isolation-test Isolation-5M-$$test); \
	done
	@echo ""
	@echo "=== Server Sender Tests Complete ==="

## test-isolation-sender-all: Run ALL sender isolation tests (~15 min)
## sudo make test-isolation-sender-all
test-isolation-sender-all: server client-generator
	@echo "=== ALL Lockless Sender Isolation Tests ==="
	@for test in CG-SendBtree Server-SendBtree \
		CG-SendRing Server-SendRing \
		CG-SendControlRing Server-SendControlRing \
		CG-SendEventLoop Server-SendEventLoop \
		Full-SendEventLoop \
		SendEL-IoUrRecv SendEL-RecvRing SendEL-RecvEL \
		SendEL-LowBackoff SendEL-HighBackoff \
		CGOnly-SendEL ServerOnly-SendEL; do \
		echo ""; \
		echo "=== Testing: Isolation-5M-$$test ==="; \
		(cd contrib/integration_testing && go run . isolation-test Isolation-5M-$$test); \
	done
	@echo ""
	@echo "=== All Sender Isolation Tests Complete ==="

## test-isolation-sender-quick: Run quick sender sanity test (single test, ~30s)
## sudo make test-isolation-sender-quick
test-isolation-sender-quick: server client-generator
	@echo "=== Quick Sender EventLoop Test ==="
	@cd contrib/integration_testing && go run . isolation-test Isolation-5M-CG-SendEventLoop

## test-isolation-sender-20m-debug: Run 20M debug tests for intermittent failure analysis
## These tests help diagnose the intermittent startup race condition at 20 Mb/s
## sudo make test-isolation-sender-20m-debug
test-isolation-sender-20m-debug: server client-generator
	@echo "=== 20M SendEventLoop Debug Tests (Intermittent Failure Analysis) ==="
	@echo "Running multiple configurations to identify race condition..."
	@for test in 20M-SendEventLoop-Debug 20M-SendEventLoop-SlowBackoff 20M-SendEventLoop-FastBackoff 20M-SendEventLoop-NoTSBPD; do \
		echo ""; \
		echo "=== Testing: Isolation-$$test ==="; \
		(cd contrib/integration_testing && go run . isolation-test Isolation-$$test) || true; \
		echo "=== $$test complete ==="; \
	done

## test-isolation-sender-20m-repeat: Run the 20M test N times to measure failure rate
## Usage: sudo make test-isolation-sender-20m-repeat ITERATIONS=10
## Default: 5 iterations
## Detection: Fails if >1000 packets dropped as too_old (normal run ~192, failure ~53000)
ITERATIONS ?= 5
test-isolation-sender-20m-repeat: server client-generator
	@echo "=== 20M SendEventLoop Repeat Test ($(ITERATIONS) iterations) ==="
	@echo "Detection: FAIL if >1000 packets dropped as too_old"
	@passes=0; fails=0; \
	for i in $$(seq 1 $(ITERATIONS)); do \
		echo ""; \
		echo "=== Run $$i of $(ITERATIONS) ==="; \
		(cd contrib/integration_testing && PRINT_PROM=true go run . isolation-test Isolation-20M-SendEventLoop) 2>&1 | tee /tmp/20m-run-$$i.log; \
		drops=$$(grep 'congestion_send_data_drop_total.*test-cg.*too_old' /tmp/20m-run-$$i.log | grep -oE '[0-9]+$$' || echo 0); \
		if [ -z "$$drops" ]; then drops=0; fi; \
		if [ "$$drops" -gt 1000 ]; then \
			echo "❌ Run $$i: FAILED ($$drops packets dropped as too_old)"; \
			fails=$$((fails + 1)); \
		else \
			echo "✅ Run $$i: PASSED ($$drops packets dropped)"; \
			passes=$$((passes + 1)); \
		fi; \
	done; \
	echo ""; \
	echo "=== Summary ==="; \
	echo "Passed: $$passes / $(ITERATIONS)"; \
	echo "Failed: $$fails / $(ITERATIONS)"; \
	if [ $(ITERATIONS) -gt 0 ]; then echo "Failure rate: $$((fails * 100 / $(ITERATIONS)))%"; fi

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

## test-congestion-live: Run congestion/live package tests
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

## ci-race: Run comprehensive race detection for CI (fails on any race)
## This target is designed for CI pipelines - exits non-zero if races found.
ci-race:
	@echo "=== CI Race Detection (Full Suite) ==="
	@echo "Running race detection on all packages..."
	@go test -race -timeout 5m ./... 2>&1 | tee /tmp/race_results.txt; \
	if grep -q "WARNING: DATA RACE" /tmp/race_results.txt; then \
		echo ""; \
		echo "❌ DATA RACE DETECTED - CI build should fail"; \
		echo "Review /tmp/race_results.txt for details"; \
		exit 1; \
	else \
		echo "✅ No races detected - CI passed"; \
	fi

## ═══════════════════════════════════════════════════════════════════════════
## CODE COVERAGE ENFORCEMENT
## ═══════════════════════════════════════════════════════════════════════════

## coverage: Generate coverage report (summary only, excludes tools/)
coverage:
	@echo "═══════════════════════════════════════════════════════════════════"
	@echo "                    CODE COVERAGE ANALYSIS"
	@echo "═══════════════════════════════════════════════════════════════════"
	@go test -coverprofile=coverage.out -covermode=atomic $$(go list ./... | grep -v /tools/) 2>&1 | grep -E "coverage:|no test files"
	@echo "───────────────────────────────────────────────────────────────────"
	@go tool cover -func=coverage.out | tail -1
	@echo "═══════════════════════════════════════════════════════════════════"
	@echo "Run 'make coverage-html' for detailed HTML report"
	@echo "Run 'make coverage-by-package' for per-package breakdown"

## coverage-html: Generate HTML coverage report (excludes tools/)
coverage-html:
	@go test -coverprofile=coverage.out -covermode=atomic $$(go list ./... | grep -v /tools/) > /dev/null 2>&1
	@go tool cover -html=coverage.out -o coverage.html
	@echo "✅ Coverage report generated: coverage.html"

## coverage-check: Enforce minimum coverage threshold (blocks on failure)
## Usage: make coverage-check [THRESHOLD=30]
## Note: Excludes tools/ from coverage calculation
coverage-check:
	@echo "=== Code Coverage Check ==="
	@go test -coverprofile=coverage.out -covermode=atomic $$(go list ./... | grep -v /tools/) > /dev/null 2>&1 || true
	@COVERAGE=$$(go tool cover -func=coverage.out | tail -1 | awk '{print $$3}' | sed 's/%//'); \
	THRESHOLD=$${THRESHOLD:-30}; \
	echo "Coverage: $$COVERAGE% (threshold: $$THRESHOLD%)"; \
	if [ $$(echo "$$COVERAGE < $$THRESHOLD" | bc -l) -eq 1 ]; then \
		echo "❌ Coverage below threshold"; \
		exit 1; \
	else \
		echo "✅ Coverage meets threshold"; \
	fi

## coverage-by-package: Show coverage by package (excludes tools/)
coverage-by-package:
	@echo "═══════════════════════════════════════════════════════════════════"
	@echo "                  COVERAGE BY PACKAGE"
	@echo "═══════════════════════════════════════════════════════════════════"
	@go test -coverprofile=coverage.out -covermode=atomic $$(go list ./... | grep -v /tools/) 2>&1 | \
		grep -E "^ok.*coverage:" | \
		sed 's/github.com\/randomizedcoder\/gosrt/./g' | \
		awk '{for(i=1;i<=NF;i++) if($$i ~ /coverage:/) print $$2, $$(i+1)}' | \
		sort | \
		awk '{printf "  %-45s %s\n", $$1, $$2}'
	@echo "───────────────────────────────────────────────────────────────────"
	@go tool cover -func=coverage.out | tail -1 | awk '{printf "  %-45s %s\n", "TOTAL", $$3}'
	@echo "═══════════════════════════════════════════════════════════════════"

## ═══════════════════════════════════════════════════════════════════════════
## UNIFIED CI PIPELINE
## ═══════════════════════════════════════════════════════════════════════════

## ci: Full CI pipeline (static checks + tests + race detection)
ci: check test ci-race
	@echo ""
	@echo "════════════════════════════════════════"
	@echo "✅ CI Pipeline Passed"
	@echo "════════════════════════════════════════"

## ci-full: Extended CI pipeline (includes coverage check)
ci-full: ci coverage-check
	@echo ""
	@echo "════════════════════════════════════════"
	@echo "✅ Full CI Pipeline Passed"
	@echo "════════════════════════════════════════"

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

## test-memory-pool: Test memory stability with duplicate packets (verifies sync.Pool works)
test-memory-pool:
	@echo "=== Memory Pool Stability Tests ==="
	@echo "Testing that duplicate packets are correctly returned to sync.Pool..."
	go test -v ./congestion/live/receive -run 'TestMemoryStability|TestBtreeInsertDuplicate|TestListVsBtree' -count=1

## bench-memory-pool: Benchmark duplicate packet handling and memory pool return
bench-memory-pool:
	@echo "=== Memory Pool Benchmarks (Duplicate Packet Handling) ==="
	go test -bench='BenchmarkDuplicatePacketPoolReturn|BenchmarkMixedPacketPoolReturn' -benchmem -count=3 ./congestion/live/receive -run='^$$'

## bench-circular: Benchmark circular number comparison functions (Lt vs LtBranchless)
bench-circular:
	@echo "=== Circular Number Comparison Benchmarks ==="
	go test -bench=BenchmarkLt -benchmem -benchtime=2s ./circular | tee /tmp/bench-circular.txt
	@echo ""
	@echo "Results saved to /tmp/bench-circular.txt"

## fuzz: Run fuzz tests
fuzz:
	go test -fuzz=Fuzz -run=^Fuzz ./packet -fuzztime 30s

## ═══════════════════════════════════════════════════════════════════════════
## DEBUG BUILD TARGETS (Lock-Free Context Verification)
## ═══════════════════════════════════════════════════════════════════════════
##
## WHAT ARE CONTEXT ASSERTS?
## -------------------------
## Context asserts verify that lock-free functions are called from the correct
## execution context (EventLoop vs Tick). This prevents subtle bugs where:
##   - A lock-free function (designed for single-threaded EventLoop) is called
##     from multi-threaded Tick path, causing data races
##   - A locking wrapper function (designed for Tick) is called from EventLoop,
##     causing unnecessary lock overhead or potential deadlocks
##
## HOW THEY WORK:
## --------------
## 1. Debug builds (go build -tags debug) include runtime context tracking:
##    - EnterEventLoop() / ExitEventLoop() - marks EventLoop context
##    - EnterTick() / ExitTick() - marks Tick context
##
## 2. Functions call AssertEventLoopContext() or AssertTickContext() at entry:
##    - If context is wrong, the assert PANICS with a clear error message
##    - In release builds, these are no-op stubs (zero overhead)
##
## ASSERT COVERAGE:
## ----------------
## Connection level (connection_debug.go):
##   - handleACKACK()           → AssertEventLoopContext (lock-free ACKACK)
##   - handleACKACKLocked()     → AssertTickContext (locking wrapper)
##   - handleKeepAliveEventLoop() → AssertEventLoopContext (lock-free keepalive)
##   - handleKeepAlive()        → AssertTickContext (locking wrapper)
##
## Sender level (congestion/live/send/debug.go):
##   - processControlPacketsDelta() → AssertEventLoopContext
##   - drainRingToBtreeEventLoop()  → AssertEventLoopContext
##   - deliverReadyPacketsEventLoop() → AssertEventLoopContext
##   - dropOldPacketsEventLoop()    → AssertEventLoopContext
##
## Receiver level (congestion/live/receive/debug_context.go):
##   - processOnePacket()       → AssertEventLoopContext
##   - periodicACK()            → AssertEventLoopContext
##   - periodicACKLocked()      → AssertTickContext
##   - periodicNakBtreeLocked() → AssertTickContext
##
## References:
##   - lockless_sender_implementation_plan.md Step 7.5.2
##   - multi_iouring_design.md Section 5.5 (Context Assert Analysis)
##   - completely_lockfree_receiver.md Section 5.7.5
## ═══════════════════════════════════════════════════════════════════════════

## test-debug: Run ALL debug assertion tests (with race detector)
## This catches EventLoop/Tick context violations at runtime
## Includes: connection, sender, and receiver assert tests
test-debug:
	@echo "=== Running ALL debug assertion tests (with race detector) ==="
	@echo "Testing connection-level context asserts..."
	go test -tags debug -race -v -run "TestConnectionDebugContext" .
	@echo ""
	@echo "Testing sender context asserts..."
	go test -tags debug -race -v ./congestion/live/send/...
	@echo ""
	@echo "Testing receiver context asserts..."
	go test -tags debug -race -v ./congestion/live/receive/...
	@echo "✅ All debug assertion tests passed"

## test-debug-quick: Quick debug test (no race detector, faster)
test-debug-quick:
	@echo "=== Quick debug assertion tests (no race detector) ==="
	go test -tags debug -v -run "TestConnectionDebugContext" .
	go test -tags debug -v ./congestion/live/send/...
	go test -tags debug -v ./congestion/live/receive/...
	@echo "✅ Quick debug tests passed"

## test-debug-connection: Test only connection-level context asserts
## Use this to verify handleACKACK/handleKeepAlive context enforcement
test-debug-connection:
	@echo "=== Testing connection-level context asserts ==="
	go test -tags debug -v -run "TestConnectionDebugContext" .
	@echo "✅ Connection context asserts verified"

## build-debug: Build server/client with debug assertions enabled
## These binaries will PANIC if context violations occur at runtime
build-debug:
	@echo "=== Building with debug assertions ==="
	@echo "NOTE: These binaries will panic on lock-free context violations"
	cd contrib/server && CGO_ENABLED=0 go build -tags debug -o server-debug -gcflags="all=-N -l" -a
	cd contrib/client && CGO_ENABLED=0 go build -tags debug -o client-debug -gcflags="all=-N -l" -a
	cd contrib/client-generator && CGO_ENABLED=0 go build -tags debug -o client-generator-debug -gcflags="all=-N -l" -a
	@echo "✅ Debug binaries built (use server-debug, client-debug, client-generator-debug)"

## verify-lockfree-context: Verify all context assertions compile correctly
## Checks both debug builds (with asserts) and release builds (with stubs)
verify-lockfree-context:
	@echo "=== Verifying lock-free context assertions ==="
	@echo "Checking connection debug builds..."
	go build -tags debug .
	@echo "Checking send package debug builds..."
	go build -tags debug ./congestion/live/send/...
	@echo "Checking receive package debug builds..."
	go build -tags debug ./congestion/live/receive/...
	@echo ""
	@echo "Checking release builds (stub functions - zero overhead)..."
	go build .
	go build ./congestion/live/send/...
	go build ./congestion/live/receive/...
	@echo "✅ Lock-free context assertions verified (both debug and release)"

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

## ═══════════════════════════════════════════════════════════════════════════
## TIERED LINTING
## ═══════════════════════════════════════════════════════════════════════════
## Tier 0: Quick (~30s) - Development feedback
## Tier 1: Standard (~2min) - PR validation (CI gating)
## Tier 2: Comprehensive (~10min) - Nightly CI
##
## See .golangci*.yml for linter configurations

## lint-quick: Tier 0 quick lint (~30s) - development feedback
## Runs: gofmt, goimports, govet, errcheck, ineffassign, unused
## Also runs: seq-audit (SRT-specific sequence arithmetic safety)
lint-quick: code-audit-seq
	@echo "=== Tier 0: Quick Lint ==="
	golangci-lint run --config .golangci-quick.yml --timeout 1m ./...
	@echo "✅ Tier 0 lint passed"

## lint: Tier 1 standard lint (~2min) - PR validation
## Includes Tier 0 plus: gosec, gosimple, gocritic, revive, contextcheck
## Also runs: metrics-audit (Prometheus metrics alignment)
lint: lint-quick audit-metrics
	@echo "=== Tier 1: Standard Lint ==="
	golangci-lint run --config .golangci.yml --timeout 5m ./...
	@echo "✅ Tier 1 lint passed"

## lint-comprehensive: Tier 2 comprehensive lint (~10min) - nightly CI
## Includes Tier 1 plus: exhaustive, prealloc, gocyclo, funlen, goconst, dupl, etc.
## Also runs: all custom audit tools
lint-comprehensive: lint
	@echo "=== Tier 2: Comprehensive Lint ==="
	golangci-lint run --config .golangci-comprehensive.yml --timeout 15m ./...
	make lint-audit-all
	@echo "✅ Tier 2 lint passed"

## lint-fix: Auto-fix linting issues where possible
lint-fix:
	golangci-lint run --fix --config .golangci.yml ./...

## lint-new: Only lint changes since last commit (fast for incremental dev)
lint-new:
	golangci-lint run --new-from-rev=HEAD~1 --config .golangci.yml ./...

## lint-audit-all: Run all custom audit tools
lint-audit-all: code-audit-seq audit-metrics code-audit-test
	@echo "✅ All custom audits passed"

## lint-staticcheck: Legacy staticcheck (deprecated, use 'make lint' instead)
lint-staticcheck:
	staticcheck ./...

## ============================================================================
## NIX FLAKE CHECKS (Sandboxed, Reproducible)
## ============================================================================
## These targets run linting/testing via nix flake check.
## Benefits: reproducible environment, no local tool version drift.
## Requires: nix with flakes enabled.

## nix-lint-quick: Run Tier 0 lint via nix (~30s)
## gofmt, govet, errcheck, ineffassign, unused
nix-lint-quick:
	nix build .#checks.x86_64-linux.golangci-lint-quick --print-build-logs
	@echo "✅ Nix Tier 0 lint passed"

## nix-lint: Run Tier 1 lint via nix (~2min) - CI gating
## Tier 0 + gosec, gosimple, gocritic, revive, contextcheck
nix-lint:
	nix build .#checks.x86_64-linux.golangci-lint --print-build-logs
	@echo "✅ Nix Tier 1 lint passed"

## nix-lint-all: Run Tier 2 comprehensive lint via nix (~10min)
## Tier 1 + gocyclo, prealloc, funlen, goconst, dupl
nix-lint-all:
	nix build .#checks.x86_64-linux.golangci-lint-comprehensive --print-build-logs
	@echo "✅ Nix Tier 2 lint passed"

## nix-sec: Run security scan via nix (gosec)
nix-sec:
	nix build .#checks.x86_64-linux.go-sec --print-build-logs
	@echo "✅ Nix security scan passed"

## nix-test: Run all Go tests via nix
nix-test:
	nix build .#checks.x86_64-linux.go-test-quick --print-build-logs
	nix build .#checks.x86_64-linux.go-test-circular --print-build-logs
	nix build .#checks.x86_64-linux.go-test-packet --print-build-logs
	@echo "✅ Nix tests passed"

## nix-audit: Run all audit tools via nix
nix-audit:
	nix build .#checks.x86_64-linux.seq-audit --print-build-logs
	nix build .#checks.x86_64-linux.metrics-audit --print-build-logs
	@echo "✅ Nix audits passed"

## nix-check: Run ALL nix flake checks (~20min)
## Includes: linting (all tiers), testing, security, audits
nix-check:
	nix flake check --print-build-logs
	@echo "✅ All nix flake checks passed"

.PHONY: nix-lint-quick nix-lint nix-lint-all nix-sec nix-test nix-audit nix-check

## client: Build import binary
client:
	cd contrib/client && CGO_ENABLED=0 go build -o client -ldflags="-s -w" -a

## client-debug: Build client binary with debug symbols and no inlining (for profiling)
client-debug:
	cd contrib/client && CGO_ENABLED=0 go build -o client-debug -gcflags="all=-N -l" -a

## client-all: Build both client and client-debug binaries
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

## ============================================================================
## PERFORMANCE TESTING (No sudo required!)
## ============================================================================
## Automated performance testing to find maximum sustainable throughput.
## Uses AIMD (Additive Increase, Multiplicative Decrease) search algorithm.
## See documentation/performance_maximization_500mbps.md for details.

## client-seeker: Build client-seeker binary (rate-adaptive traffic generator)
client-seeker:
	cd contrib/client-seeker && CGO_ENABLED=0 go build -o client-seeker -ldflags="-s -w" -a

## performance: Build performance orchestrator binary
performance:
	cd contrib/performance && CGO_ENABLED=0 go build -o performance -ldflags="-s -w" -a

## build-performance: Build all performance testing binaries
.PHONY: build-performance
build-performance: server client-seeker performance

## test-performance: Run automated performance search (no sudo required!)
## Usage: make test-performance
## Options:
##   INITIAL=200M    - Starting bitrate
##   MAX=600M        - Maximum bitrate to test
##   STEP=10M        - Additive increase step
##   PRECISION=5M    - Search precision
##   FC=102400       - Flow control window
##   RECV_RINGS=2    - Number of receive io_uring rings
##   VERBOSE=true    - Enable verbose output
##   JSON=true       - Output results as JSON
## Example:
##   make test-performance INITIAL=100M MAX=400M FC=204800
.PHONY: test-performance
test-performance: build-performance
	./contrib/performance/performance $(PERF_ARGS)

## test-performance-quick: Quick performance test (smaller range, faster)
## Good for CI or quick sanity checks
.PHONY: test-performance-quick
test-performance-quick: build-performance
	./contrib/performance/performance INITIAL=100M MAX=200M STEP=20M PRECISION=10M TIMEOUT=2m $(PERF_ARGS)

## test-backoff-hypothesis: Test if sleep is the EventLoop bottleneck
## Compares iteration rates: NoWait vs Yield vs Spin vs Sleep
## Result: Sleep caps at ~945/sec, Yield achieves 6.2M/sec (6581x faster!)
.PHONY: test-backoff-hypothesis
test-backoff-hypothesis:
	go test -v -timeout 30s -run TestBackoffHypothesis ./congestion/live/send/

## bench-backoff: Benchmark different backoff modes
.PHONY: bench-backoff
bench-backoff:
	go test -bench=BenchmarkBackoffModes -benchtime=1s ./congestion/live/send/

## test-performance-500: Full performance test targeting 500 Mb/s
## Warning: May take 10+ minutes
.PHONY: test-performance-500
test-performance-500: build-performance
	./contrib/performance/performance INITIAL=200M MAX=600M STEP=10M FC=204800 RECV_RINGS=4 VERBOSE=true $(PERF_ARGS)

## test-performance-dry-run: Validate configuration without running
.PHONY: test-performance-dry-run
test-performance-dry-run: build-performance
	./contrib/performance/performance -dry-run $(PERF_ARGS)

## clean-performance: Remove performance testing binaries and sockets
.PHONY: clean-performance
clean-performance:
	rm -f contrib/client-seeker/client-seeker
	rm -f contrib/performance/performance
	rm -f /tmp/srt_*.sock

## server-profile: Open pprof web UI for server CPU profile
server-profile:
	go tool pprof -http=0.0.0.0:8080 ./contrib/server/server-debug cpu.pprof

## server-all: Build both server and server-debug binaries
server-all: server server-debug

## clean: Remove all built binaries (forces rebuild on next test)
clean:
	@echo "Removing built binaries..."
	rm -f contrib/server/server contrib/server/server-debug
	rm -f contrib/client/client contrib/client/client-debug
	rm -f contrib/client-generator/client-generator contrib/client-generator/client-generator-debug
	rm -f contrib/client-seeker/client-seeker
	rm -f contrib/performance/performance
	@echo "Clean complete. Run 'make client server client-generator' to rebuild."

## clean-all: Remove all built binaries and generated files
clean-all: clean
	rm -f cover.out cover.html
	rm -f cpu.pprof mem.pprof

## rebuild: Clean and rebuild all binaries
rebuild: clean client server client-generator

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
.PHONY: test-race test-race-wraparound test-race-eventloop ci-race test-circular test-packet-store
# Network impairment testing targets (require root)
.PHONY: test-network-list test-network test-network-all test-network-quick network-setup network-cleanup network-status
# Parallel comparison testing targets (require root)
.PHONY: test-parallel-list test-parallel test-parallel-all integration-testing
.PHONY: test-parallel-sender test-parallel-sender-full test-parallel-sender-high
.PHONY: test-parallel-sender-loss test-parallel-sender-starlink test-parallel-sender-all
# Isolation testing targets (require root)
.PHONY: test-isolation-list test-isolation test-isolation-all test-isolation-strategies
.PHONY: test-isolation-sender-list test-isolation-sender-phases test-isolation-sender-server
# Performance testing targets (no sudo required!)
.PHONY: client-seeker performance build-performance clean-performance
.PHONY: test-isolation-sender-all test-isolation-sender-quick
.PHONY: test-isolation-sender-20m-debug test-isolation-sender-20m-repeat
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
# Debug build targets (lock-free context verification)
.PHONY: test-debug test-debug-quick test-debug-connection build-debug verify-lockfree-context
# Code quality targets
.PHONY: vet fmt lint lint-quick lint-comprehensive lint-fix lint-new lint-audit-all lint-staticcheck
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
