# Burrow Architecture

## System Overview

Burrow is a concurrent Kafka consumer library that processes messages in parallel while maintaining **at-least-once delivery guarantees** and **ordered offset commits**. This document explains the architectural design and key components.

## Architecture Diagram

```
┌─────────────────────────────────────────────────────────────────────┐
│                         Kafka Cluster                                │
│                    (topic with N partitions)                         │
└────────────────────────────┬────────────────────────────────────────┘
                             │
                             │ Poll (single-threaded)
                             ↓
┌─────────────────────────────────────────────────────────────────────┐
│                            Pool                                      │
│                      (Main Orchestrator)                             │
│  - Creates and coordinates all components                           │
│  - Manages lifecycle                                                 │
└──────────┬──────────────────┬──────────────────┬─────────────────────┘
           │                  │                  │
           ↓                  ↓                  ↓
  ┌────────────────┐  ┌──────────────┐  ┌──────────────┐
  │ Worker Pool    │  │ Commit       │  │ Error        │
  │ (N workers)    │  │ Manager      │  │ Tracker      │
  └────────┬───────┘  └──────┬───────┘  └──────┬───────┘
           │                 │                  │
           │                 │                  │
           ↓                 ↓                  ↓
  ┌─────────────────────────────────────────────────────┐
  │          Results Channel (thread-safe)              │
  └──────────────────┬──────────────────────────────────┘
                     │
                     ↓
         ┌───────────────────────────┐
         │    Result Processor       │
         │  (handles completion)     │
         └───────────┬───────────────┘
                     │
                     ↓
         ┌───────────────────────────────────────────┐
         │  Offset Trackers (per partition)         │
         │  - Track processed offsets                │
         │  - Detect gaps                            │
         │  - Compute committable offset             │
         └───────────┬───────────────────────────────┘
                     │
                     ↓
         ┌───────────────────────────┐
         │    Kafka Commits          │
         │  (synchronous, ordered)   │
         └───────────────────────────┘
```

## Core Components

### 1. Pool (Orchestrator)

**Responsibility**: Main entry point that creates and coordinates all other components.

**Key Operations**:
- Initialize worker pool, commit manager, error tracker
- Run single-threaded Kafka poll loop
- Dispatch jobs to workers
- Coordinate graceful shutdown

**Thread Safety**: Single poll thread, multiple result processors.

**Code Location**: `pool.go`

### 2. Worker Pool

**Responsibility**: Manages a fixed pool of goroutines that process messages concurrently.

**Key Operations**:
- Maintain N worker goroutines
- Pull jobs from buffered channel
- Execute user's ProcessFunc
- Send results to results channel

**Concurrency**: N goroutines pulling from shared channel (natural load balancing).

**Backpressure**: Bounded channels block when full.

**Code Location**: `worker.go`

### 3. Offset Tracker (per partition)

**Responsibility**: Track which offsets have been processed and compute safe-to-commit offset.

**Key Data Structures**:
```go
type OffsetTracker struct {
    processedMap    map[int64]bool  // offset → completed?
    inflightMap     map[int64]bool  // offset → currently processing?
    lastCommitted   int64           // Last offset committed to Kafka
    highWatermark   int64           // Highest offset seen
}
```

**Core Algorithm - Gap Detection**:
```go
func GetCommittableOffset() int64 {
    committable := lastCommitted
    for offset := lastCommitted + 1; offset <= highWatermark; offset++ {
        if !processedMap[offset] {
            break  // Gap! Can't commit beyond this
        }
        committable = offset
    }
    return committable
}
```

**Thread Safety**: Protected by mutex (accessed from multiple goroutines).

**Code Location**: `tracker.go`

### 4. Commit Manager

**Responsibility**: Periodically commit offsets to Kafka in correct order.

**Key Operations**:
- Query all trackers for committable offsets
- Batch commits (avoid per-message overhead)
- Use synchronous commits (safety over throughput)
- Trigger commits on timer OR batch size threshold

**Commit Strategy**:
```go
// Periodic (e.g., every 5 seconds)
ticker := time.NewTicker(commitInterval)

// OR batch size (e.g., every 1000 messages)
if messagesSinceCommit >= batchSize {
    commit()
}
```

**Code Location**: `commit.go`

### 5. Error Tracker

**Responsibility**: Track errors and halt processing if threshold exceeded.

**Key Operations**:
- Count consecutive errors
- Reset counter on success
- Return "should halt" signal when threshold reached

**Pattern** (inspired by Eddies):
```go
consecutiveErrors++
if consecutiveErrors >= maxConsecutive {
    return true  // Halt!
}
```

**Code Location**: `errors.go`

## Data Flow

### Happy Path (Successful Processing)

```
1. Kafka Message Arrives
   └─> Pool polls message

2. Create Job
   └─> Wrap message with metadata (partition, offset)

3. Track Inflight
   └─> OffsetTracker.RecordInflight(offset)

4. Submit to Worker Pool
   └─> job → jobsChan (may block if full)

5. Worker Processes
   └─> Pull from jobsChan
   └─> Execute processFunc(msg)
   └─> Send result → resultsChan

6. Result Processor Handles Success
   └─> OffsetTracker.MarkProcessed(offset)
   └─> ErrorTracker.RecordSuccess()

7. Commit Manager (periodic)
   └─> Query tracker: GetCommittableOffset()
   └─> Commit to Kafka (synchronous)
   └─> Update tracker: CommitOffset(offset)
```

### Failure Path (Failed Processing)

```
1-5. Same as happy path

6. Worker Processes
   └─> processFunc(msg) returns error

7. Result Processor Handles Failure
   └─> Check if retriable

   If retriable (attempt < maxRetries):
       └─> Schedule retry with backoff
       └─> Re-submit job to workers

   Else (permanent failure):
       └─> OffsetTracker.MarkFailed(offset)  ← Leaves gap!
       └─> ErrorTracker.RecordError()
       └─> Check threshold
           If exceeded: Halt pool

8. Commit Manager (periodic)
   └─> GetCommittableOffset() stops at gap
   └─> Only commits up to last contiguous offset
```

### Gap Example

```
Offsets processed: [0, 1, 2, 4, 5, 6]
                            ^-- Gap at 3!

GetCommittableOffset() returns: 2

Kafka commit: offset 3 (next to read)
              └─ Meaning: "I've processed 0, 1, 2. Start from 3 next time."

Offset 3 remains uncommitted (gap).
On restart: Offset 3 will be reprocessed.
```

## Concurrency Model

### Single-Threaded Components
- **Kafka Poll Loop**: Consumer is NOT thread-safe
- Must poll from single goroutine

### Multi-Threaded Components
- **Worker Pool**: N goroutines processing concurrently
- **Result Processor**: Separate goroutine handling results
- **Commit Manager**: Separate goroutine for periodic commits

### Synchronization Primitives

1. **Channels** (primary):
   - `jobsChan`: Poll → Workers
   - `resultsChan`: Workers → Result Processor
   - Bounded for backpressure

2. **Mutexes** (selective):
   - OffsetTracker: Protects processedMap, inflightMap
   - Pool: Protects offsetTrackers map

3. **Context** (cancellation):
   - Propagate shutdown signal
   - Graceful termination

## Memory Management

### Potential Memory Issues

1. **Unbounded map growth** in OffsetTracker:
   ```go
   processedMap[offset] = true  // Grows indefinitely!
   ```

2. **Solution - Cleanup after commit**:
   ```go
   func CommitOffset(offset int64) {
       // Delete old entries
       for o := range processedMap {
           if o <= offset {
               delete(processedMap, o)
           }
       }
   }
   ```

3. **Channel buffer sizes**:
   - Jobs channel: Config.JobQueueSize (default: 1000)
   - Results channel: Config.ResultQueueSize (default: 1000)
   - Bounded = prevents unbounded memory growth

### Memory Growth Characteristics

**Best Case**: O(W) where W = number of workers
- All messages process quickly
- Minimal entries in processedMap

**Worst Case**: O(P × L) where P = partitions, L = lag
- Large offset gaps (failed messages)
- High lag between consumption and commit

**Mitigation**:
- Periodic commits (clear old entries)
- Error threshold (halt if too many failures)
- Monitor memory usage (metrics)

## Partition Rebalance Handling

When Kafka rebalances partitions:

```
1. Partitions Revoked
   └─> Wait for inflight messages to complete
       └─> OffsetTracker.WaitForInflight()

   └─> Final commit before losing partition
       └─> CommitManager.tryCommit()

   └─> Cleanup trackers
       └─> Delete from offsetTrackers map
       └─> Unregister from CommitManager

2. Partitions Assigned
   └─> Trackers created lazily on first message
```

**Critical**: Must wait for inflight messages before committing!

## Error Handling Philosophy

### Transient vs. Permanent Errors

**Transient** (retry):
- Network timeouts
- Database connection errors
- API rate limits
- Temporary service unavailability

**Permanent** (don't retry):
- Invalid message format
- Authorization failures
- Business logic violations
- Messages that will never succeed

### Retry Strategy

```
Attempt 1: 100ms backoff
Attempt 2: 200ms backoff
Attempt 3: 400ms backoff
Attempt N: min(base × 2^(N-1), 60s)
```

### Error Threshold

Inspired by Eddies:

```
consecutiveErrors = 0

On error:
    consecutiveErrors++
    if consecutiveErrors >= maxConsecutive:
        Halt pool!

On success:
    consecutiveErrors = 0  # Reset
```

**Rationale**: Prevents runaway failures from consuming resources.

## Performance Characteristics

### Throughput

**Theoretical Max**:
```
Messages/sec = NumWorkers × (1 / AvgProcessingTime)
```

**Example**:
- 100 workers
- 50ms avg processing time
- Max: 100 × (1/0.05) = 2,000 messages/sec

**Bottlenecks**:
- I/O latency (database, API calls)
- Commit overhead (periodic, not per-message)
- Backpressure (if workers can't keep up)

### Latency

**End-to-end latency**:
```
Total = PollTime + QueueTime + ProcessingTime + CommitTime
```

**Components**:
- PollTime: ~100ms (Kafka read timeout)
- QueueTime: Depends on backpressure (usually <1ms)
- ProcessingTime: User's ProcessFunc (varies)
- CommitTime: Periodic (not per-message)

**Head-of-line blocking**: One slow message blocks commits for all subsequent messages.

### Scalability

**Horizontal** (more consumers):
- Add more consumer instances
- Each gets subset of partitions
- Limited by partition count

**Vertical** (more workers per consumer):
- Increase NumWorkers
- More concurrent processing
- Limited by CPU/memory

**Optimal**: NumWorkers ≈ 2-4 × CPU cores for I/O-bound work

## Trade-offs

### Advantages ✅
- **High throughput**: Concurrent processing
- **At-least-once**: No message loss
- **Fail-safe**: Failed messages block commits
- **Backpressure**: Bounded queues prevent OOM
- **Simple API**: Drop-in for standard consumer

### Disadvantages ❌
- **Head-of-line blocking**: Slow message blocks commits
- **Memory overhead**: Must track all inflight offsets
- **Duplicate processing**: On restart, some messages reprocessed
- **Complexity**: More parts than single-threaded

### When to Use

**Good fit** ✅:
- I/O-heavy operations
- High latency variance
- At-least-once acceptable
- Message loss unacceptable

**Poor fit** ❌:
- CPU-bound operations
- Exactly-once required
- Sub-millisecond latency critical

## Monitoring & Observability

### Key Metrics (Prometheus)

```
burrow_messages_processed_total{partition, status}
burrow_processing_duration_seconds{partition}
burrow_offsets_committed_total{partition}
burrow_offset_lag{partition}
burrow_inflight_messages{partition}
burrow_worker_utilization
burrow_errors_total{partition, retriable}
```

### Health Checks

```go
type HealthCheck struct {
    Healthy          bool
    WorkersActive    int
    PartitionsActive int
    OffsetLag        map[int32]int64
    ErrorRate        float64
}
```

### Alerts

- **High lag**: Offset lag > threshold
- **Error rate**: Errors > threshold
- **Worker utilization**: All workers busy
- **Rebalance frequency**: Too many rebalances

## Comparison with Alternatives

### vs. Single-Threaded Consumer
- **Burrow**: 100x throughput (100 workers)
- **Single**: Simple, no gaps, lower memory

### vs. Multiple Consumer Instances
- **Burrow**: More workers per partition
- **Multiple**: Limited by partition count

### vs. Confluent Parallel Consumer (Java)
- **Burrow**: Go-native, simpler API
- **Confluent**: More features, mature, Java-only

### vs. Eddies (Node.js Streams)
- **Burrow**: Kafka-specific, ordered commits
- **Eddies**: Stream-generic, no ordering

## Future Enhancements

### Potential Features

1. **Ordered Processing Mode**:
   - Process messages in order per partition
   - Trade throughput for ordering

2. **Dead Letter Queue**:
   - Publish permanently failed messages to DLQ topic
   - Prevent gap accumulation

3. **Dynamic Worker Scaling**:
   - Auto-scale workers based on lag
   - React to load changes

4. **Transaction Support**:
   - Exactly-once semantics
   - For produce + consume workflows

5. **Compression**:
   - Compress processedMap for large gaps
   - Reduce memory footprint

---

**Architecture Version**: 1.0
**Last Updated**: 2025-01-10
