# Development Notes

Important learnings and constraints during development of AMQProxy.

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
