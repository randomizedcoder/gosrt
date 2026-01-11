#!/etc/profiles/per-user/das/bin/bash
# NAK Investigation Tests - Sequential execution with output capture
# Run from gosrt directory with: sudo bash -c 'source /tmp/nak_tests.sh'

set -e
cd /home/das/Downloads/srt/gosrt

echo "=== NAK Investigation Test Suite ==="
echo "Started: $(date)"
echo ""

# Test 1: EventLoop WITHOUT io_uring
echo "[1/4] Running Isolation-5M-EventLoop-NoIOUring..."
make test-isolation CONFIG=Isolation-5M-EventLoop-NoIOUring PRINT_PROM=true 2>&1 | tee /tmp/nak-test-1-EventLoop-NoIOUring.log
echo "Test 1 complete: /tmp/nak-test-1-EventLoop-NoIOUring.log"
echo ""

# Test 2: io_uring recv + NAK btree in Tick mode
echo "[2/4] Running Isolation-5M-Server-NakBtree-IoUr..."
make test-isolation CONFIG=Isolation-5M-Server-NakBtree-IoUr PRINT_PROM=true 2>&1 | tee /tmp/nak-test-2-NakBtree-IoUr.log
echo "Test 2 complete: /tmp/nak-test-2-NakBtree-IoUr.log"
echo ""

# Test 3: Full receiver EventLoop at 5Mbps (no sender EventLoop)
echo "[3/4] Running Isolation-5M-FullEventLoop..."
make test-isolation CONFIG=Isolation-5M-FullEventLoop PRINT_PROM=true 2>&1 | tee /tmp/nak-test-3-FullEventLoop-5M.log
echo "Test 3 complete: /tmp/nak-test-3-FullEventLoop-5M.log"
echo ""

# Test 4: Full receiver EventLoop at 20Mbps
echo "[4/4] Running Isolation-20M-FullEventLoop..."
make test-isolation CONFIG=Isolation-20M-FullEventLoop PRINT_PROM=true 2>&1 | tee /tmp/nak-test-4-FullEventLoop-20M.log
echo "Test 4 complete: /tmp/nak-test-4-FullEventLoop-20M.log"
echo ""

echo "=== All Tests Complete ==="
echo "Finished: $(date)"
echo ""
echo "Results summary (NAKs sent by test server):"
for f in /tmp/nak-test-*.log; do
    name=$(basename "$f" .log)
    naks=$(grep -E 'connection_nak_entries_total.*test-server.*single' "$f" 2>/dev/null | grep -oE '[0-9]+$' || echo "0")
    echo "  $name: $naks NAKs"
done
