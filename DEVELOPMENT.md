# Development Notes

Important learnings and constraints during development of AMQProxy.

## Code Review Fixes (2025-02-19)

### Critical Issues Fixed

**1. Certificate Writing Bug**
- **File:** tests/certs.go
- **Problem:** Writing private keys to .crt files instead of certificates
- **Impact:** TLS connections would fail
- **Fix:** Write cert PEM to .crt and key PEM to .key

**2. Frame Size Validation**
- **File:** proxy/frame.go
- **Problem:** ParseFrame allocated any size without limit
- **Impact:** DoS vulnerability - malicious client could cause OOM
- **Fix:** Add MaxFrameSize constant (1MB limit) and validate

**3. Frame End Marker Validation**
- **File:** proxy/frame.go
- **Problem:** Not validating required 0xCE end marker
- **Impact:** Protocol non-compliance, invalid frames accepted
- **Fix:** Read and validate frame end marker in ParseFrame, write it in WriteFrame

**4. Thread Safety - Writer Race Condition** (frame_proxy.go:10, 25, 35, 48, 58)
- **Problem:** FrameProxy.Writer was *bytes.Buffer with NO synchronization
- **Impact:** Multiple goroutines would corrupt buffer
- **Fix:** Add mutex to FrameProxy, protect all Writer access

**5. Reset() Function Corruption** (frame_proxy.go:22-25)
- **Problem:** Reset() function had wrong body (WriteFrame call)
- **Impact:** Would write wrong data instead of clearing buffer
- **Fix:** Implement proper Reset() that calls bytes.Buffer.Reset()

### Important Issues Fixed

**6. Error Handling**
- **File:** tests/certs.go
- **Problem:** Multiple errors silently ignored (os.MkdirAll, MarshalPKCS8PrivateKey)
- **Impact:** Silent failures, difficult debugging
- **Fix:** Return errors from WriteCerts function

**7. O(n) Reverse Lookup** (connection.go:48-54, frame_proxy.go:48-54)
- **Problem:** Iterating entire ChannelMapping to find client channel for upstream
- **Impact:** O(n) per frame instead of O(1), problematic at scale
- **Fix:** Add ReverseMapping to ClientConnection, maintain both directions

**8. Silent Frame Loss** (frame_proxy.go:26, 52)
- **Problem:** Returns nil for unmapped channels, caller can't distinguish success/failure
- **Impact:** Can drop frames on unmapped channels
- **Fix:** Document behavior, return nil for unmapped (documented as expected)

**9. Incorrect Zero Check** (frame_proxy.go:56)
- **Problem:** `if clientID == 0` - channel 0 IS valid for connection methods
- **Impact:** Would drop valid frames
- **Fix:** Use boolean `found` flag instead of zero check

**10. Buffer Type Issue** (frame_proxy.go:10)
- **Problem:** Writer was *bytes.Buffer (pointer), but bytes.Buffer has value receivers
- **Impact:** Type mismatch with io.Writer interface
- **Fix:** Use bytes.Buffer (value type) not pointer

### Test Coverage Improvements

**11. Frame Proxy Test Coverage** (frame_proxy_test.go)
- **Before:** Only tested ProxyClientToUpstream with empty mapping
- **After:** Added tests for:
  - ProxyClientToUpstream with mapping
  - ProxyUpstreamToClient with mapping
  - ProxyUpstreamToClient no mapping
  - FrameProxy.Reset
  - Channel 0 validation
- **Impact:** All frame proxying paths covered

## AMQP Spec Review Findings (2025-02-19)

### Critical Issue: Connection.StartOk Method ID

**Problem:** Implementation was checking for method ID 20 instead of 11.

**Context:** 
- Plan originally had incorrect AMQP information (said credentials in Connection.Open)
- First fix: Changed to Connection.StartOk, but used method ID 20
- **Issue:** Method ID 20 is Connection.Secure, not Connection.StartOk

**Correct AMQP 0-9-1 Connection Methods:**
```
Connection.Start:    class=10, method=10
Connection.StartOk:  class=10, method=11 ← Should be 11!
Connection.Secure:   class=10, method=20  ← Wrong ID was here
Connection.SecureOk:  class=10, method=21
Connection.Tune:     class=10, method=30
Connection.TuneOk:   class=10, method=31
Connection.Open:     class=10, method=40
```

**Impact:** Would reject valid Connection.StartOk frames and incorrectly parse Connection.Secure frames.

**Fix:** Changed method ID check from 20 to 11 in ParseConnectionStartOk.

**Reference:** https://www.rabbitmq.com/amqp-0-9-1-reference.html#connection.start-ok

### AMQP Spec Compliance Summary

After review, these areas are now CORRECT:
- ✅ Frame format (7-byte header, type/channel/size, end marker 0xCE)
- ✅ Frame types (Method=1, Header=2, Body=3, Heartbeat=8)
- ✅ Method headers (ClassID/MethodID as 16-bit big-endian)
- ✅ Data types (shortstr=1-byte+len, longstr=4-byte+len, table)
- ✅ Connection.StartOk fields (table, shortstr, longstr, shortstr)
- ✅ PLAIN auth format (\0auth-id\0username\0password)

## Code Review Fixes (2025-02-19)

### Critical Issues Fixed

**1. Certificate Writing Bug**
- **File:** tests/certs.go
- **Problem:** Writing private keys to .crt files instead of certificates
- **Impact:** TLS connections would fail
- **Fix:** Write cert PEM to .crt and key PEM to .key

**2. Frame Size Validation**
- **File:** proxy/frame.go
- **Problem:** ParseFrame allocated any size without limit
- **Impact:** DoS vulnerability - malicious client could cause OOM
- **Fix:** Add MaxFrameSize constant (1MB limit) and validate

**3. Frame End Marker Validation**
- **File:** proxy/frame.go
- **Problem:** Not validating required 0xCE end marker
- **Impact:** Protocol non-compliance, invalid frames accepted
- **Fix:** Read and validate frame end marker in ParseFrame, write it in WriteFrame

### Important Issues Fixed

**4. Error Handling**
- **File:** tests/certs.go
- **Problem:** Multiple errors silently ignored (os.MkdirAll, MarshalPKCS8PrivateKey)
- **Impact:** Silent failures, difficult debugging
- **Fix:** Return errors from WriteCerts function

## Git Parallel Execution Issue

### Problem
When running multiple `git` commands in parallel (e.g., in a single bash tool call), they collide trying to access `.git/index`, causing:

```
fatal: Unable to create '/Users/tim/.../.git/index.lock': File exists.
Another git process seems to be running...
```

### Root Cause
- OpenCode executes bash commands in parallel when batched
- Git creates lock files (`index.lock`) to prevent conflicts
- Parallel git commands collide and leave lock files behind

### Solution
**Never batch git commands together.** Run them sequentially or add delays between them.

**Bad:**
```bash
git status
git diff
git log
```

**Good:**
```bash
git status && \
git diff && \
git log
```

**Or run them one at a time in separate tool calls.**

### Fix When Locked
If lock file appears:
```bash
rm -f .git/index.lock
git <command>
```

## AMQP 0-9-1 Specification Notes

### Connection Handshake
1. Client sends protocol header (`AMQP\x00\x00\x09\x01`)
2. Server sends Connection.Start (class 10, method 10)
3. **Client sends Connection.StartOk (class 10, method 20)** ← **CREDENTIALS HERE**
4. Server sends Connection.Tune (class 10, method 30)
5. Client sends Connection.TuneOk (class 10, method 31)
6. Client sends Connection.Open (class 10, method 40) ← **VHOST HERE**
7. Server sends Connection.OpenOk

### Key Point
- **Credentials** are in Connection.StartOk's `response` field (longstr)
- PLAIN auth format: `\0auth-id\0username\0password`
- Connection.Open only specifies which vhost to connect to (after auth)

### Reference
https://www.rabbitmq.com/amqp-0-9-1-reference.html#connection.start-ok

## Tech Debt

### Task 10 - Enhanced Connection Pool (2025-02-19)

**1. IsSafeChannel Documentation (pool/pool.go:82)**
- **Type:** Documentation
- **Item:** IsSafeChannel returns false for non-existent channels (Go map zero value)
- **Context:** This is correct behavior - unmarked channels are unsafe by default
- **Action:** Add comment explaining zero-value semantics for clarity
- **Priority:** Low - implementation is correct, just needs documentation

**2. Test Coverage Expansion (pool/pool_test.go:23-30)**
- **Type:** Testing
- **Item:** TestSafeChannelManagement only covers happy path
- **Context:** Currently tests add/remove single channel
- **Action:** Add edge cases:
  - Multiple channels added/checked
  - Removing non-existent channel (idempotent)
  - Checking non-existent channel (returns false)
- **Priority:** Low - basic functionality tested, edge cases not critical

**3. LastUsed Update Timing (pool/pool.go:82)**
- **Type:** Feature
- **Item:** LastUsed only set when safe channel is added
- **Context:** May need updates in other operations:
  - When connection is used for operations
  - When channels are closed
  - For idle timeout calculations
- **Action:** Update LastUsed in relevant places as needed
- **Priority:** TBD - depends on idle timeout implementation requirements

## Code Review Fixes (2025-02-19)

### Critical Issues Fixed

**11. Buffer Not Flushed (connection.go:108, 143, 147)**
- **Problem:** Methods writing to buffered Writer didn't call Flush()
- **Impact:** Frames would sit in buffer, AMQP handshake would hang
- **Fix:** Added cc.Writer.Flush() after WriteFrame in:
  - sendConnectionStart()
  - sendConnectionTune()
  - Handle() after Connection.OpenOK
- **Context:** bufio.Writer buffers writes; must flush to actually send data

## Tech Debt

### Task 12 - Proxy Stop Method (2025-02-19)

**1. Integration Test Port Conflicts (tests/integration_tls_test.go)**
- **Type:** Testing
- **Item:** TestTLSConnection and TestConnectionMultiplexing both use port 15673
- **Context:** When running all tests together, second test may fail due to port conflict
- **Action:** Use different ports or random ports for each test
- **Priority:** Medium - tests work individually but fail when run together

**2. ClientConnection.Handle() Not Integrated (proxy/proxy.go)**
- **Type:** Feature
- **Item:** handleConnection doesn't call ClientConnection.Handle()
- **Context:** Task 11 implemented full AMQP flow in ClientConnection.Handle() but proxy.go's handleConnection only parses protocol header and returns dummy credentials
- **Action:** Integrate ClientConnection.Handle() into proxy.go's handleConnection
- **Priority:** High - required for actual proxying functionality

### Task 13 - Integration Tests (2025-02-19)

**3. Port Conflict in Multiplexing Test (integration_tls_test.go:93, 67)**
- **Type:** Testing
- **Item:** Both TestTLSConnection and TestConnectionMultiplexing use port 15673
- **Context:** Running all tests together causes "address already in use" errors
- **Action:** Implement getAvailablePort() or use different fixed ports per test
- **Priority:** Medium - tests fail in full test suite (mitigated by backoff)

**4. Test Doesn't Verify Multiplexing (integration_tls_test.go:75-127)**
- **Type:** Testing
- **Item:** Test only checks that 3 TLS connections can be established
- **Context:** Named "TestConnectionMultiplexing" but doesn't verify:
  - Single upstream connection is shared by multiple clients
  - Channel remapping works
  - Safe channel behavior
- **Plan note:** "Verify single upstream connection was created // TODO: Implement verification"
- **Current test:** Only asserts `assert.Equal(t, 3, len(conns))` - not multiplexing specific
- **Action:** Add assertions to check pool.Connections count, verify single upstream connection
- **Priority:** High - test doesn't validate the feature it claims to test

**5. Race Condition in TestTLSConnection (integration_tls_test.go:67-73)**
- **Type:** Testing
- **Item:** Fixed wait (100ms) + no nil check causes panic on slow systems
- **Context:** If proxy startup is slow, TLS connection attempts before ready, leaving conn in bad state
- **Status:** FIXED - Added exponential backoff (10 retries, 50-500ms) + nil pointer check before defer
- **Impact:** Eliminates flaky test failures and nil pointer dereference panic
- **Fix:** Exponential backoff for connection attempts, nil check before defer
- **Priority:** High - critical for production reliability

