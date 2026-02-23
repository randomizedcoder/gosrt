# Client No-Output Mode Design

## Overview

Add a "no-output" mode to the client that allows it to connect to an SRT server, perform all SRT repair work (packet reordering, retransmission, etc.), but discard the output packets instead of writing them anywhere. This is useful for:

1. **Profiling**: Easier profiling without needing to pipe to external tools like `ffplay`
2. **Testing**: Testing SRT connection quality and repair mechanisms without consuming output
3. **Benchmarking**: Measuring SRT performance without I/O overhead from writing data

## Current Implementation

### Current Flow

```
main()
  ├─ openReader(*from)  → Reads from SRT server
  ├─ openWriter(*to)    → Writes to destination (required)
  └─ Copy loop:
       Read from reader → Write to writer
```

**Key Points**:
- `-to` flag is **required** (currently fails if empty)
- `openWriter()` returns error if address is empty
- Write errors in the copy loop cause the client to exit

### Current Code Structure

```go
// Flag definition
to = flag.String("to", "", "Address to write to, targets: srt://, udp://, file://, - (stdout)")

// Writer initialization (required)
w, err := openWriter(*to, logger)
if err != nil {
    fmt.Fprintf(os.Stderr, "Error: to: %v\n", err)
    flag.PrintDefaults()
    os.Exit(1)
}

// Copy loop
go func() {
    for {
        n, err := r.Read(buffer)
        if err != nil {
            doneChan <- fmt.Errorf("read: %w", err)
            return
        }
        s.update(uint64(n))

        if _, err := w.Write(buffer[:n]); err != nil {  // ← Always writes
            doneChan <- fmt.Errorf("write: %w", err)
            return
        }
    }
}()
```

## Proposed Solution

### Option 1: Special "null" or "discard" Destination (Recommended) ⭐

Add a special destination value that creates a no-op writer.

**Implementation**:
1. Make `-to` flag optional (allow empty string)
2. Add special handling for `-to ""` or `-to "null"` or `-to "discard"`
3. Create a `NullWriter` that implements `io.WriteCloser` but discards all data
4. Modify copy loop to handle null writer gracefully

**Pros**:
- ✅ Clean API - explicit "discard" destination
- ✅ Backward compatible (existing usage unchanged)
- ✅ Clear intent in command line
- ✅ Easy to implement

**Cons**:
- ⚠️ Need to handle empty string case (currently errors)

**Example Usage**:
```bash
# Connect and discard output
./client -from "srt://server:6001/stream" -to "null"

# Or make -to optional when empty
./client -from "srt://server:6001/stream" -to ""
```

### Option 2: New `-no-output` Flag

Add a separate boolean flag to enable no-output mode.

**Implementation**:
1. Add `-no-output` boolean flag
2. When set, ignore `-to` flag (or make it optional)
3. Use `NullWriter` when `-no-output` is true

**Pros**:
- ✅ Very explicit and clear
- ✅ Doesn't change existing `-to` behavior

**Cons**:
- ⚠️ Two ways to do the same thing (flag vs empty `-to`)
- ⚠️ More flags to maintain

**Example Usage**:
```bash
./client -from "srt://server:6001/stream" -no-output
```

### Option 3: Make `-to` Optional (Simplest)

Simply make `-to` optional and use a null writer when not provided.

**Implementation**:
1. Make `-to` flag optional (no error if empty)
2. When `-to` is empty, use `NullWriter`
3. Update `openWriter()` to return `NullWriter` for empty string

**Pros**:
- ✅ Simplest implementation
- ✅ Most intuitive (no `-to` = no output)

**Cons**:
- ⚠️ Changes behavior (currently `-to` is required)
- ⚠️ Less explicit than special value

**Example Usage**:
```bash
# Simply omit -to flag
./client -from "srt://server:6001/stream"
```

## Recommended Implementation: Option 1 (Special "null" Destination)

### Design Details

#### 1. NullWriter Implementation

```go
// NullWriter is an io.WriteCloser that discards all data
type NullWriter struct{}

func (n *NullWriter) Write(p []byte) (int, error) {
    return len(p), nil  // Discard data, return success
}

func (n *NullWriter) Close() error {
    return nil  // No-op close
}
```

#### 2. Modify `openWriter()` Function

```go
func openWriter(addr string, logger srt.Logger) (io.WriteCloser, error) {
    // Handle empty string or special "null"/"discard" values
    if len(addr) == 0 || addr == "null" || addr == "discard" {
        return &NullWriter{}, nil
    }

    // Existing implementation for other addresses...
    // (unchanged)
}
```

#### 3. Update Flag Documentation

```go
to = flag.String("to", "", "Address to write to, targets: srt://, udp://, file://, - (stdout), null (discard output)")
```

#### 4. Update Copy Loop (No Changes Needed)

The existing copy loop will work as-is:
- `w.Write()` will succeed (NullWriter always succeeds)
- No write errors will occur
- Statistics will still be collected (reads still happen)

### Implementation Steps

1. **Add NullWriter type** to `contrib/client/main.go`
2. **Modify `openWriter()`** to handle empty/null/discard
3. **Update flag help text** to document null destination
4. **Update error handling** - remove requirement for `-to` flag
5. **Test**:
   - Verify SRT connection still works
   - Verify statistics are still collected
   - Verify profiling works correctly
   - Verify no write errors occur

### Code Changes

#### File: `contrib/client/main.go`

**Add NullWriter**:
```go
// NullWriter is an io.WriteCloser that discards all data.
// Useful for profiling and testing SRT connections without output overhead.
type NullWriter struct{}

func (n *NullWriter) Write(p []byte) (int, error) {
    return len(p), nil
}

func (n *NullWriter) Close() error {
    return nil
}
```

**Modify openWriter()**:
```go
func openWriter(addr string, logger srt.Logger) (io.WriteCloser, error) {
    // Handle no-output mode: empty string, "null", or "discard"
    if len(addr) == 0 || addr == "null" || addr == "discard" {
        return &NullWriter{}, nil
    }

    // Rest of existing implementation...
    if addr == "-" {
        // ... existing stdout handling ...
    }
    // ... existing file://, srt://, udp:// handling ...
}
```

**Update flag documentation**:
```go
to = flag.String("to", "", "Address to write to, targets: srt://, udp://, file://, - (stdout), null (discard output, useful for profiling)")
```

**Update error handling** (if needed):
```go
// openWriter now never returns error for empty/null/discard
// Existing error handling should work as-is
```

### Usage Examples

#### Profiling with No Output
```bash
# Connect to server, do SRT repair, but discard output
~/Downloads/gosrt/contrib/client/client-debug \
    -profile cpu \
    -iouringenabled \
    -iouringrecvenabled \
    -conntimeo 3000 \
    -rcvlatency 3000 \
    -peerlatency 3000 \
    -peeridletimeo 30000 \
    -tlpktdrop \
    -packetreorderalgorithm btree \
    -from "srt://172.16.40.46:6001/?mode=caller&streamid=/live/stream" \
    -to null

# Profile will be written when you hit Ctrl+C
# No need to pipe to ffplay!
```

#### Testing Connection Quality
```bash
# Test SRT connection without consuming output
./client \
    -from "srt://server:6001/stream" \
    -to null \
    -logtopics "connection,recv"
```

#### Benchmarking
```bash
# Measure SRT performance without I/O overhead
./client \
    -from "srt://server:6001/stream" \
    -to null
```

### Benefits

1. **Easier Profiling**: No need to pipe to external tools
2. **Cleaner Testing**: Test SRT functionality without output complexity
3. **Better Benchmarking**: Measure SRT performance without I/O overhead
4. **Backward Compatible**: Existing usage unchanged
5. **Explicit Intent**: Clear that output is being discarded

### Edge Cases to Handle

1. **Statistics Collection**: ✅ Still works (reads still happen)
2. **Connection Stats**: ✅ Still collected (reader is SRT connection)
3. **Writer Stats**: ⚠️ NullWriter has no stats (handle gracefully)
4. **Error Handling**: ✅ NullWriter never errors
5. **Close()**: ✅ NullWriter.Close() is safe to call

### Testing Checklist

- [ ] Client connects to server with `-to null`
- [ ] Client performs SRT repair (packet reordering, retransmission)
- [ ] Statistics are collected correctly
- [ ] No write errors occur
- [ ] Profiling works correctly
- [ ] Ctrl+C exits cleanly and writes profile
- [ ] Connection stats are printed at end
- [ ] Backward compatibility: existing `-to` usage still works

### Alternative: Option 3 (Make `-to` Optional)

If we prefer the simplest approach, we can just make `-to` optional:

**Changes**:
1. Remove error when `-to` is empty
2. Use `NullWriter` when `-to` is empty
3. Update documentation

**Usage**:
```bash
# Simply omit -to
./client -from "srt://server:6001/stream" -profile cpu
```

This is simpler but less explicit. Option 1 (special "null" value) is recommended for clarity.

## Recommendation

**Implement Option 1**: Add support for `-to null` or `-to ""` to enable no-output mode.

**Rationale**:
- ✅ Explicit and clear intent
- ✅ Backward compatible
- ✅ Easy to implement
- ✅ Good for profiling use case
- ✅ Follows existing pattern (special values like `-` for stdin/stdout)

This will make profiling much easier - you can simply run:
```bash
./client -from "srt://..." -to null -profile cpu
```

And hit Ctrl+C to get the profile, without needing to pipe to `ffplay` or other tools.

