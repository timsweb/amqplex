# Development Notes

Important learnings and constraints during development of AMQProxy.

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
