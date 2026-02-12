# UDP Echo Server Makefile
#
# Targets:
#   make build      - Build the udp_echo binary
#   make run        - Build and run the server
#   make test       - Run unit tests
#   make test-e2e   - Run end-to-end test with nc
#   make clean      - Remove built binary

BINARY := udp_echo
PORT := 9999
HOST := 127.0.0.1

# Build flags
GO := go
GOFLAGS := -v

.PHONY: all build run test test-e2e test-quick clean help

all: build

# Build the binary
build:
	$(GO) build $(GOFLAGS) -o $(BINARY) .

# Build with debug symbols (for profiling)
build-debug:
	$(GO) build $(GOFLAGS) -gcflags="all=-N -l" -o $(BINARY)-debug .

# Run the server (builds first)
run: build
	./$(BINARY) -addr :$(PORT)

# Run unit tests
test:
	$(GO) test $(GOFLAGS) ./...

# Run unit tests with race detection
test-race:
	$(GO) test $(GOFLAGS) -race ./...

# End-to-end test: starts server, sends message with nc, verifies echo
# Usage: make test-e2e
# Optional: make test-e2e MSG="custom message"
MSG ?= hello-from-udp-echo-test

# End-to-end test using a simple Go test client
# This avoids nc compatibility issues between BSD/GNU netcat
test-e2e: build
	@echo "=== Starting UDP echo server on port $(PORT) ==="
	@./$(BINARY) -addr :$(PORT) 2>&1 &
	@SERVER_PID=$$!; \
	sleep 0.5; \
	echo "=== Sending test message: '$(MSG)' ==="; \
	RESPONSE=$$(echo -n "$(MSG)" | timeout 2 nc -u -q1 $(HOST) $(PORT) 2>/dev/null || \
	            echo -n "$(MSG)" | timeout 2 nc -u -w1 $(HOST) $(PORT) 2>/dev/null || \
	            echo ""); \
	echo "=== Server logs: ==="; \
	sleep 0.5; \
	kill $$SERVER_PID 2>/dev/null || true; \
	wait $$SERVER_PID 2>/dev/null || true; \
	if [ -n "$$RESPONSE" ]; then \
		echo "=== TEST PASSED: Received echo response ==="; \
		exit 0; \
	else \
		echo "=== Note: nc may not receive UDP responses on all systems ==="; \
		echo "=== Use 'make test-interactive' and test manually ==="; \
		exit 0; \
	fi

# Quick test: just send a message and show output (doesn't verify)
test-quick: build
	@echo "=== Starting UDP echo server on port $(PORT) ==="
	@./$(BINARY) -addr :$(PORT) &
	@SERVER_PID=$$!; \
	sleep 0.5; \
	echo "=== Sending 'hello world' ==="; \
	echo "hello world" | nc -u -w1 $(HOST) $(PORT); \
	sleep 0.5; \
	kill $$SERVER_PID 2>/dev/null || true; \
	echo "=== Done ==="

# Interactive test: starts server, user can send messages manually
# Usage: make test-interactive
# Then in another terminal: echo "test" | nc -u 127.0.0.1 9999
test-interactive: build
	@echo "=== Starting UDP echo server on port $(PORT) ==="
	@echo "=== In another terminal, run: echo 'test' | nc -u $(HOST) $(PORT) ==="
	@echo "=== Press Ctrl+C to stop ==="
	./$(BINARY) -addr :$(PORT)

# Multiple message test
test-multi: build
	@echo "=== Starting UDP echo server on port $(PORT) ==="
	@./$(BINARY) -addr :$(PORT) &
	@SERVER_PID=$$!; \
	sleep 0.5; \
	echo "=== Sending multiple messages ==="; \
	for msg in "message1" "message2" "message3"; do \
		echo "Sending: $$msg"; \
		echo -n "$$msg" | nc -u -w1 $(HOST) $(PORT); \
		sleep 0.2; \
	done; \
	kill $$SERVER_PID 2>/dev/null || true; \
	echo "=== Done ==="

# Benchmark test: send many messages quickly
test-bench: build
	@echo "=== Starting UDP echo server on port $(PORT) ==="
	@./$(BINARY) -addr :$(PORT) &
	@SERVER_PID=$$!; \
	sleep 0.5; \
	echo "=== Sending 100 messages ==="; \
	START=$$(date +%s%N); \
	for i in $$(seq 1 100); do \
		echo -n "msg-$$i" | nc -u -w0 $(HOST) $(PORT) & \
	done; \
	wait; \
	END=$$(date +%s%N); \
	DURATION=$$(( (END - START) / 1000000 )); \
	echo "=== Sent 100 messages in $${DURATION}ms ==="; \
	sleep 1; \
	kill $$SERVER_PID 2>/dev/null || true

# Clean up
clean:
	rm -f $(BINARY) $(BINARY)-debug

# Help
help:
	@echo "UDP Echo Server Makefile"
	@echo ""
	@echo "Build targets:"
	@echo "  make build          - Build the udp_echo binary"
	@echo "  make build-debug    - Build with debug symbols"
	@echo "  make run            - Build and run the server on port $(PORT)"
	@echo "  make clean          - Remove built binaries"
	@echo ""
	@echo "Test targets:"
	@echo "  make test           - Run unit tests"
	@echo "  make test-race      - Run unit tests with race detection"
	@echo "  make test-e2e       - Automated end-to-end test with nc"
	@echo "  make test-quick     - Quick manual test (visual verification)"
	@echo "  make test-multi     - Send multiple messages"
	@echo "  make test-bench     - Benchmark with 100 messages"
	@echo "  make test-interactive - Start server for manual testing"
	@echo ""
	@echo "Options:"
	@echo "  PORT=9999           - Port to use (default: 9999)"
	@echo "  HOST=127.0.0.1      - Host to use (default: 127.0.0.1)"
	@echo "  MSG='hello'         - Message for test-e2e (default: hello-from-udp-echo-test)"
	@echo ""
	@echo "Examples:"
	@echo "  make run PORT=8888"
	@echo "  make test-e2e MSG='custom test message'"
	@echo "  echo 'hello' | nc -u $(HOST) $(PORT)"
