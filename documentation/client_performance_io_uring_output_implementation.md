# Client Performance: Output Writer Implementation

## Overview

This document tracks the implementation progress of **Priority 2: Eliminate NonblockingWriter Entirely** from `client_performance_analysis.md`.

## Related Documents

- `client_performance_analysis.md` - Parent design document
- `client_performance_stats_improvement_implementation.md` - Priority 1 (completed)
- `zero_copy_opportunities.md` - Advanced zero-copy optimizations

---

## Problem Statement

The current `NonblockingWriter` in `contrib/client/writer.go` uses:
- `sync.RWMutex` for buffer protection
- A polling loop to check for writes
- Channel-based (channels internally use locks)

This introduces lock contention in the hot path, which is inefficient for high-throughput streaming.

### Current NonblockingWriter Issues

```go
type NonblockingWriter struct {
    w       io.WriteCloser
    lock    sync.RWMutex    // ⚠️ Lock per write
    buffer  []byte
    closed  bool
}
```

At 10 Mb/s with ~250 packets/second:
- **250 lock acquisitions per second** just for writing
- Additional overhead from polling loop

---

## Solution: Two-Phase Approach

### Phase 1: DirectWriter (No unsafe, stdlib only) ✅

Replace `NonblockingWriter` with simple `os.File.Write()`:
- ✅ Zero locks in our code
- ✅ Zero channels
- ✅ Uses battle-tested stdlib
- ✅ Works on all platforms
- ✅ No `unsafe` package

### Phase 2: Optional io_uring Output (Linux, advanced) 🔲

Add CLI flag `-iouringoutput` to enable io_uring async writes:
- Isolate `unsafe` code in `contrib/common/writer_iouring_linux.go`
- Clear comments explaining why `unsafe` is needed
- Optional feature for users who need absolute minimum latency

---

## Phase 1: DirectWriter Implementation

**Status:** ✅ Complete

**Goal:** Replace `NonblockingWriter` with simple `os.File.Write()` - zero locks, zero unsafe.

### Design Comparison

```
Before (NonblockingWriter):
┌─────────────────────────────────────────────────────────┐
│ Write() → lock.Lock() → buffer → polling → syscall     │
│           ⚠️ LOCK        ⚠️ COPY   ⚠️ LOOP              │
└─────────────────────────────────────────────────────────┘

After (DirectWriter):
┌─────────────────────────────────────────────────────────┐
│ Write() → os.File.Write() → syscall                    │
│           ✅ No lock       ✅ Direct                    │
└─────────────────────────────────────────────────────────┘
```

### Files to Create/Modify

| File | Action | Description |
|------|--------|-------------|
| `contrib/common/writer.go` | Create | DirectWriter implementation |
| `contrib/client/main.go` | Modify | Use DirectWriter instead of NonblockingWriter |

### Step 1.1: Create DirectWriter in contrib/common/writer.go

**Status:** ✅ Complete

```go
// writer.go - High-performance writers for output destinations
// No unsafe package - uses stdlib only
package common

import (
    "io"
    "os"
)

// NullWriter discards all data (for profiling/benchmarking)
type NullWriter struct{}

func (n *NullWriter) Write(p []byte) (int, error) { return len(p), nil }
func (n *NullWriter) Close() error                 { return nil }

// DirectWriter wraps os.File for zero-overhead writes
// - No locks in our code
// - No channels
// - Single syscall per write
// - Uses battle-tested stdlib
type DirectWriter struct {
    file      *os.File
    closeFile bool // Whether to close the file on Close()
}

// NewDirectWriter creates a writer for the given file
func NewDirectWriter(f *os.File, closeOnClose bool) *DirectWriter {
    return &DirectWriter{
        file:      f,
        closeFile: closeOnClose,
    }
}

// NewStdoutWriter creates a writer for stdout
func NewStdoutWriter() *DirectWriter {
    return &DirectWriter{
        file:      os.Stdout,
        closeFile: false, // Don't close stdout
    }
}

// NewFileWriter creates a writer for a new file
func NewFileWriter(path string) (*DirectWriter, error) {
    f, err := os.Create(path)
    if err != nil {
        return nil, err
    }
    return &DirectWriter{
        file:      f,
        closeFile: true,
    }, nil
}

func (w *DirectWriter) Write(p []byte) (int, error) {
    return w.file.Write(p)
}

func (w *DirectWriter) Close() error {
    if w.closeFile && w.file != nil {
        return w.file.Close()
    }
    return nil
}

// Fd returns the file descriptor (useful for io_uring upgrade path)
func (w *DirectWriter) Fd() int {
    if w.file == nil {
        return -1
    }
    return int(w.file.Fd())
}
```

### Step 1.2: Update client to use DirectWriter

**Status:** ✅ Complete

Replaced `NonblockingWriter` usage with `DirectWriter` in `contrib/client/main.go`:
- `-to -` (stdout): Uses `common.NewStdoutWriter()`
- `-to file://path`: Uses `common.NewFileWriter(path)`
- `-to null`: Uses `common.NullWriter{}`

### Step 1.3: Verify with integration test

**Status:** ✅ Complete

Integration test passed: `make test-integration` → PASSED

---

## Phase 2: Optional io_uring Output Writer

**Status:** ✅ Complete

**Goal:** Add optional io_uring async writes for advanced users who need absolute minimum latency.

### CLI Flag (Implemented)

```
$ ./client -h | grep iouringoutput
  -iouringoutput
    	Enable io_uring for output writes (Linux only, ADVANCED).
        Uses the 'unsafe' package for io_uring's zero-copy interface.
        The unsafe code is isolated in contrib/common/writer_iouring_linux.go.
        For most use cases, the default DirectWriter is recommended.
```

### File Organization

```
contrib/common/
├── writer.go                    # DirectWriter (no unsafe)
├── writer_iouring_linux.go      # IoUringWriter (uses unsafe, Linux only)
└── writer_iouring_stub.go       # Stub for non-Linux (returns error)
```

### Why unsafe is Required

io_uring requires passing buffer addresses directly to the kernel:

```go
// The kernel needs the actual memory address of the buffer.
// This cannot be done without unsafe.Pointer because:
// 1. Go's []byte is a slice header (ptr, len, cap) not a raw pointer
// 2. io_uring's SQE expects a uintptr for the buffer address
// 3. The buffer must not move during the async operation
sqe.PrepareWrite(fd, uintptr(unsafe.Pointer(&buf[0])), uint32(len(buf)), 0)
```

### Usage Examples

```bash
# Default (DirectWriter - no unsafe, recommended)
./client -from srt://server:6000/stream -to -

# With io_uring output (Linux only, uses unsafe, for advanced users)
./client -from srt://server:6000/stream -to - -iouringoutput

# File output with io_uring
./client -from srt://server:6000/stream -to file://output.ts -iouringoutput
```

### Files Created

| File | Description |
|------|-------------|
| `contrib/common/writer_iouring_linux.go` | IoUringWriter with extensive comments |
| `contrib/common/writer_iouring_stub.go` | Stub for non-Linux platforms |

### Code Documentation

The `writer_iouring_linux.go` file includes extensive comments at file level:
- Why unsafe is required for io_uring
- How buffer safety is ensured via sync.Pool
- The async write pattern (submit → kernel executes → completion handler)
- Safety measures and references to proven patterns in connection_linux.go

---

## Implementation Checklist

### Phase 1: DirectWriter (No unsafe)
- [x] Design DirectWriter API
- [x] Create `contrib/common/writer.go`
- [x] Update `contrib/client/main.go` to use DirectWriter
- [x] Test with `-to -` (stdout) - via integration test
- [x] Test with `-to null` - via integration test
- [x] Run integration test - PASSED
- [x] Update documentation

### Phase 2: IoUringWriter (Optional, uses unsafe)
- [x] Add `-iouringoutput` CLI flag to `contrib/common/flags.go`
- [x] Create `contrib/common/writer_iouring_linux.go`
- [x] Create `contrib/common/writer_iouring_stub.go`
- [x] Update `contrib/client/main.go` to use IoUringWriter when flag is set
- [x] Test build on Linux - PASSED
- [x] Integration test - PASSED
- [x] Document unsafe usage (extensive comments in writer_iouring_linux.go)

---

## Change Log

| Date | Phase | Change | Status |
|------|-------|--------|--------|
| 2024-12-06 | - | Created implementation document | 📝 |
| 2024-12-06 | - | Updated to two-phase approach (DirectWriter first) | 📝 |
| 2024-12-06 | 1 | Created `contrib/common/writer.go` with DirectWriter | ✅ |
| 2024-12-06 | 1 | Updated `contrib/client/main.go` to use DirectWriter | ✅ |
| 2024-12-06 | 1 | Integration test passed | ✅ |
| 2024-12-06 | 1 | **Phase 1 Complete** | ✅ |
| 2024-12-06 | 2 | Created `contrib/common/writer_iouring_linux.go` | ✅ |
| 2024-12-06 | 2 | Created `contrib/common/writer_iouring_stub.go` | ✅ |
| 2024-12-06 | 2 | Added `-iouringoutput` CLI flag | ✅ |
| 2024-12-06 | 2 | Updated client to use IoUringWriter when flag set | ✅ |
| 2024-12-06 | 2 | Build and integration test passed | ✅ |
| 2024-12-06 | 2 | **Phase 2 Complete** | ✅ |

---

## Notes

### Why DirectWriter First?

The main problem with `NonblockingWriter` was the mutex lock per write, not the blocking nature of the write syscall. A simple `os.File.Write()`:
- Has zero locks in our code
- Is a single syscall
- Is fast enough for most use cases (stdout, files)
- Works on all platforms

io_uring adds complexity and requires `unsafe`, so we only want it as an opt-in feature for users who truly need it.

### When io_uring Output Helps

io_uring async writes are beneficial when:
1. The destination is very slow (pipe to slow consumer)
2. You can't afford to block at all
3. You need absolute minimum latency

For most SRT streaming use cases (stdout to ffplay, file output), DirectWriter is sufficient.
