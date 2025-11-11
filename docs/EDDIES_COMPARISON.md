# Burrow vs. Eddies: A Detailed Comparison

## Overview

**Burrow** is inspired by [Eddies](https://github.com/Relink/eddies), a Node.js library for concurrent stream processing. This document explains what we adopted from Eddies and what we had to add for Kafka's requirements.

## Eddies Quick Summary

Eddies creates a pool of workers in Node.js streams that:
- ✅ Process tasks concurrently
- ✅ Handle backpressure via drain events
- ✅ Track errors with threshold
- ✅ Retry failed tasks
- ✅ Clean separation: Supervisor (orchestrator) + Actor (worker)

**Use case**: Concurrent processing in Node.js stream pipelines

## What Burrow Adopted from Eddies

### 1. Clean Separation Pattern ✅

**Eddies**:
```javascript
Supervisor (orchestrates)
    └─> Multiple Actors (process)
```

**Burrow**:
```go
Pool (orchestrates)
    └─> Multiple Workers (process)
```

**Why adopted**: Excellent separation of concerns!

### 2. Concurrent Worker Pool ✅

**Eddies**:
```javascript
// Spawn N actors asynchronously
_startActors(num) {
    if (num > 0) {
        setTimeout(() => actor.run(), 0)
        _startActors(num - 1)
    }
}
```

**Burrow**:
```go
// Start N worker goroutines
for i := 0; i < numWorkers; i++ {
    go worker.run(ctx)
}
```

**Why adopted**: Core pattern for concurrency!

### 3. Error Tracking with Threshold ✅

**Eddies**:
```javascript
_trackErrors(err) {
    consecutiveErrors++
    if (consecutiveErrors > maxErrors) {
        // Halt processing
    }
}
```

**Burrow**:
```go
func (et *ErrorTracker) RecordError() bool {
    et.consecutiveErrors++
    return et.consecutiveErrors >= et.maxConsecutive
}
```

**Why adopted**: Prevents runaway failures!

### 4. Recursive Processing Pattern ✅

**Eddies Actor**:
```javascript
_consume() {
    const input = src.read()
    if (!input) return Promise.resolve()  // Done

    return transform(input)
        .then(output => _write(output))
        .then(() => _consume())  // Recurse!
}
```

**Burrow Worker**:
```go
func (w *Worker) run(ctx context.Context) {
    for {
        select {
        case <-ctx.Done():
            return
        case job := <-w.jobsChan:
            w.process(job)  // Process and loop
        }
    }
}
```

**Why adopted**: Simple, elegant processing loop!

### 5. Backpressure Handling ✅

**Eddies**:
```javascript
_write(output) {
    const canWrite = dest.write(output)
    if (!canWrite) {
        // Wait for drain event
        return new Promise(resolve => {
            dest.once('drain', resolve)
        })
    }
}
```

**Burrow**:
```go
// Bounded channel provides backpressure
jobsChan := make(chan *Job, 1000)

// Blocks if channel is full
jobsChan <- job  // Backpressure!
```

**Why adopted**: Natural flow control!

### 6. Event-Based Communication ✅

**Eddies**:
```javascript
actor.on('success', () => { /* handle */ })
actor.on('error', (err) => { /* handle */ })
actor.on('end', () => { /* handle */ })
```

**Burrow**:
```go
// Workers send results via channel
resultsChan <- &Result{
    Success: true,
    Error:   nil,
}

// Pool receives and processes
for result := range resultsChan {
    if result.Success {
        // Handle success
    } else {
        // Handle error
    }
}
```

**Why adopted**: Clean communication pattern!

## What Burrow Added for Kafka

### 1. Offset Tracking per Partition ➕ NEW

**Eddies**: No concept of offsets or positions

**Burrow**:
```go
type OffsetTracker struct {
    partition       int32
    processedMap    map[int64]bool  // Which offsets completed?
    lastCommitted   int64
    highWatermark   int64
}
```

**Why needed**: Kafka requires tracking which offsets are processed!

### 2. Gap Detection ➕ NEW

**Eddies**: Order doesn't matter (streams can be processed out of order)

**Burrow**:
```go
func (ot *OffsetTracker) GetCommittableOffset() int64 {
    // Find highest contiguous offset (no gaps)
    for offset := lastCommitted + 1; offset <= highWatermark; offset++ {
        if !processedMap[offset] {
            break  // Gap! Can't commit beyond this
        }
        committable = offset
    }
    return committable
}
```

**Why needed**: Must commit offsets in order to avoid message loss!

### 3. Commit Coordination ➕ NEW

**Eddies**: No commits (stream processing doesn't need persistence)

**Burrow**:
```go
func (cm *CommitManager) commitLoop(ctx context.Context) {
    ticker := time.NewTicker(commitInterval)
    for {
        select {
        case <-ticker.C:
            cm.tryCommit()  // Periodic commit
        }
    }
}
```

**Why needed**: Kafka requires committing offsets to track progress!

### 4. Partition Rebalance Handling ➕ NEW

**Eddies**: No partitions (streams don't rebalance)

**Burrow**:
```go
func (p *Pool) onPartitionsRevoked(partitions []kafka.TopicPartition) {
    // Wait for inflight messages
    for _, tp := range partitions {
        tracker.WaitForInflight()
    }

    // Final commit
    commitManager.tryCommit()

    // Cleanup trackers
    delete(offsetTrackers, partition)
}
```

**Why needed**: Kafka consumers must handle partition reassignment!

### 5. Exponential Backoff Retry ➕ ENHANCED

**Eddies**: Simple recycle to input stream

**Burrow**:
```go
func calculateBackoff(attempt int, base time.Duration) time.Duration {
    // Exponential: base × 2^attempt, capped at 60s
    backoff := base * time.Duration(1<<uint(attempt))
    if backoff > 60*time.Second {
        backoff = 60 * time.Second
    }
    return backoff
}

// Schedule retry with backoff
time.AfterFunc(backoff, func() {
    jobsChan <- retryJob
})
```

**Why enhanced**: Better handling of transient failures!

## Architecture Comparison

### Eddies Architecture

```
Input Stream
     ↓
Supervisor
  ├─ Spawn actors
  ├─ Track errors
  └─ Handle completion
     ↓
Multiple Actors (concurrent)
  ├─ Read from stream
  ├─ Transform data
  └─ Write to stream
     ↓
Output Stream
```

### Burrow Architecture

```
Kafka Consumer
     ↓
Pool (like Supervisor)
  ├─ Poll messages
  ├─ Create trackers
  ├─ Coordinate commits
  └─ Handle errors
     ↓
Worker Pool (like Actors)
  ├─ Process messages
  └─ Send results
     ↓
Result Processor (NEW)
  └─ Update trackers
     ↓
Offset Trackers (NEW)
  └─ Detect gaps
     ↓
Commit Manager (NEW)
  └─ Commit to Kafka
```

## Key Differences

| Aspect | Eddies | Burrow |
|--------|--------|--------|
| **Purpose** | Stream processing | Kafka consumer |
| **Ordering** | ❌ Not maintained | ✅ Ordered commits |
| **Persistence** | ❌ In-memory only | ✅ Kafka offsets |
| **Offset tracking** | ❌ Not applicable | ✅ Per partition |
| **Gap detection** | ❌ Not needed | ✅ Essential |
| **Partitions** | ❌ No concept | ✅ Per-partition handling |
| **Backpressure** | ✅ Drain events | ✅ Bounded channels |
| **Concurrency** | ✅ Multiple actors | ✅ Multiple workers |
| **Error threshold** | ✅ Halt on errors | ✅ Same pattern |
| **Retry** | ✅ Recycle | ✅ Exponential backoff |
| **Language** | JavaScript | Go |
| **Runtime** | Node.js | Native |

## Lessons from Eddies

### What Worked Well

1. **Supervisor/Actor separation**: Clear responsibilities
2. **Error threshold**: Prevents cascading failures
3. **Backpressure**: Natural flow control
4. **Simple API**: Easy to use
5. **Recursive pattern**: Clean processing loop

### What We Adapted

1. **Channels instead of events**: Go's native concurrency
2. **Context for cancellation**: Go's cancellation pattern
3. **Mutexes for safety**: Thread-safe shared state
4. **Offset tracking layer**: Kafka-specific needs

## Code Comparison

### Starting Workers

**Eddies**:
```javascript
_startActors(num) {
    if (num < 1) return Promise.resolve()

    const endCb = () => --num < 1 && resolve()

    setTimeout(() => {
        this._startActor(endCb, startCb)
    }, 0)

    return this._startActors(num)
}
```

**Burrow**:
```go
func (wp *WorkerPool) Start() {
    for i := 0; i < wp.numWorkers; i++ {
        worker := &Worker{
            id:          i,
            jobsChan:    wp.jobsChan,
            resultsChan: wp.resultsChan,
            logger:      wp.logger,
        }
        go worker.run(wp.ctx)
    }
}
```

### Processing Loop

**Eddies Actor**:
```javascript
_consume() {
    const input = this.src.read()
    if (!input) {
        return Promise.resolve()
    }

    return this.transform(input, this.acc)
        .then(output => this._write(output))
        .then(() => this._consume())
        .catch(err => this._handleError(err))
}
```

**Burrow Worker**:
```go
func (w *Worker) run(ctx context.Context) {
    for {
        select {
        case <-ctx.Done():
            return
        case job := <-w.jobsChan:
            err := job.ProcessFunc(ctx, job.Message)
            w.resultsChan <- &Result{
                Success: err == nil,
                Error:   err,
            }
        }
    }
}
```

### Error Handling

**Eddies**:
```javascript
_trackErrors(err) {
    errorCnt++
    if (errorCnt > this.maxErrors) {
        this.emit('error', new Error('Max errors exceeded'))
        return false
    }
    return true
}
```

**Burrow**:
```go
func (et *ErrorTracker) RecordError(partition int32, offset int64, err error) bool {
    et.consecutiveErrors++
    shouldHalt := et.consecutiveErrors >= et.maxConsecutive

    if shouldHalt {
        et.logger.Error("error threshold exceeded, halting")
    }

    return shouldHalt
}
```

## Why Burrow Couldn't Just Use Eddies Pattern

### Problem 1: Ordering Requirements

**Eddies**: Tasks can complete in any order
```
Input:  [1, 2, 3, 4, 5]
Output: [1, 3, 5, 2, 4]  ← Order doesn't matter
```

**Kafka**: Must commit in order
```
Consumed:  [1, 2, 3, 4, 5]
Completed: [1, 3, 5, 2, 4]  ← Can only commit up to 1!
```

### Problem 2: Durability Requirements

**Eddies**: In-memory only, crash = lose state

**Kafka**: Must persist progress, restart = resume from last commit

### Problem 3: Partition Management

**Eddies**: Single stream

**Kafka**: Multiple partitions that can be reassigned

## Conclusion

### What We Kept from Eddies ✅
- Supervisor/Actor pattern
- Worker pool with concurrency
- Error tracking with threshold
- Backpressure handling
- Simple, clean API

### What We Added for Kafka ➕
- Offset tracking per partition
- Gap detection algorithm
- Commit coordination
- Partition rebalance handling
- Exponential backoff retry

### Result

**Burrow = Eddies' elegance + Kafka's guarantees**

A Go library that combines the best of both worlds:
- Clean architecture (from Eddies)
- Kafka compatibility (our addition)
- At-least-once guarantees (our addition)
- Ordered commits (our addition)

---

**Thanks to Eddies for the inspiration!** 🙏

The patterns in Eddies provided an excellent foundation for Burrow's design.
