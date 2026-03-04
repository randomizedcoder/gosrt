# Lint Issues Resolution Progress

## Summary

Started with 137 lint issues, currently at 102 issues.

### Current Status (Tier 0 - Quick Lint)

| Category | Count | Notes |
|----------|-------|-------|
| errcheck | 50 | Mostly test files, some contrib tools |
| staticcheck | 19 | SA6002 sync.Pool, SA4006 unused err, SA9003 empty branch |
| unused | 33 | Many are intentional (debug builds, future use) |
| **Total** | **102** | |

### Issues Fixed

1. **All ineffassign issues (5)** - Fixed by properly using or removing variables
2. **Production code errcheck** - Only 2 remain (file.Close patterns with documented reasoning)
3. **Timer interval validation** - Added proper error checking
4. **Connection handlers** - Changed from `_ =` to `if err != nil { t.Logf(...) }`
5. **Metrics handlers** - Implemented write helper with error accumulation

### Remaining Work by Category

#### errcheck (50 remaining)
- Test file handlers: Most are in test goroutines where `require` can't be used
- Contrib tools: Integration testing, performance tools
- Pattern: Most are `resp.Body.Close()`, `f.Close()`, `conn.Close()` in cleanup paths

#### staticcheck (19 remaining)
- **SA6002** (8): `sync.Pool` with `[]byte` instead of `*[]byte` - requires refactoring
- **SA4006** (8): Variable assigned but never used - need code review
- **SA9003** (3): Empty branches - need meaningful handling

#### unused (33 remaining)
Many are likely intentional:
- Debug context types (only used in debug builds)
- Test helper functions (may be referenced conditionally)
- Future use placeholders

### Approach Decision Needed

The user emphasized: "we should NOT disable lint checks, and we should not try to bypass the checks in other ways, we need to correctly fix the identified issues"

Regarding `_ =` pattern usage:
- **Acceptable cases**: `file.Close()` on read-only/duplicated FDs, HTTP response body close
- **Better alternatives**: `if err != nil { t.Logf(...) }` for test code, `if err != nil { log.Printf(...) }` for production

### Files Modified

Core files:
- `.golangci-quick.yml` (created)
- `.golangci.yml` (created)
- `.golangci-comprehensive.yml` (created)
- `Makefile` (lint targets added)
- `nix/checks.nix` (lint checks added)
- `nix/shell.nix` (shellHook updated)
- `CLAUDE.md` (documentation updated)

Production code:
- `server.go` - Fixed empty branch
- `listen_linux.go` - Fixed file.Close with documentation
- `net.go` - Fixed file.Close with documentation
- `contrib/client-seeker/metrics.go` - Fixed all fmt.Fprintf with error accumulation
- `contrib/client-seeker/control.go` - Fixed conn.Close with proper error handling
- `contrib/common/metrics_client.go` - Fixed resp.Body.Close
- `metrics/proc_stat_linux.go` - Fixed file.Close
- `metrics/stabilization.go` - Fixed resp.Body.Close

Test files:
- `dial_test.go` - Fixed error handling in test goroutines
- `listen_test.go` - Fixed ineffassign issues
- `connection_test.go` - Fixed handler error checking with t.Logf
- `connection_metrics_test.go` - Fixed handler error checking with t.Logf
- `connection_lifecycle_table_test.go` - Fixed ineffassign
- `contrib/client-seeker/control_test.go` - Fixed conn.Write error checking
- `contrib/client-seeker/bitrate_test.go` - Fixed bm.Set error checking
- `contrib/client-seeker/generator_test.go` - Fixed gen.Generate error checking
- `connection_io_uring_bench_test.go` - Fixed cleanup error handling
- `congestion/live/receive/tsbpd_advancement_test.go` - Fixed ineffassign

### Next Steps

1. Continue fixing test file errcheck issues with proper `if err != nil { t.Logf(...) }` pattern
2. Review and fix staticcheck SA4006 (unused error values)
3. Review unused functions - determine if intentional or dead code
4. Consider sync.Pool refactoring for SA6002 (architectural decision)
