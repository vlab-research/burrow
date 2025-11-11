# Burrow - Concurrent Kafka Consumer Library for Go

> Named after the network of interconnected tunnels where many workers operate simultaneously - just like our concurrent Kafka consumer pattern.

## Overview

**Burrow** is a Go library that provides concurrent message processing for Kafka consumers while maintaining **at-least-once delivery guarantees** and **ordered offset commits**. It combines the elegant worker pool pattern from [Eddies](https://github.com/Relink/eddies) with Kafka-specific offset tracking and commit coordination.

## The Problem

When building Kafka consumers that perform I/O-heavy operations (database queries, API calls, etc.), you face a fundamental challenge:

1. **Sequential processing is slow**: One slow message blocks everything
2. **Parallel processing breaks ordering**: If you process messages concurrently, they complete out of order
3. **Kafka requires ordered commits**: You can't commit offset 10 if offset 5 failed (you'd lose message 5 forever)

**Example:**
```
Consumed:  [0, 1, 2, 3, 4, 5]
Completed: [0, 2, 4, 5, 1, 3]  ← Out of order due to varying I/O times
Can commit: offset 0 only!      ← Must wait for 1, 2, 3... in sequence
```

If you commit offset 5, but offset 3 failed, **you lose message 3 forever** ❌

## The Solution

Burrow solves this with an **Ordered Offset Tracker** pattern:

1. **Concurrent processing**: N workers process messages in parallel
2. **Completion tracking**: Track which offsets have been processed (per partition)
3. **Gap detection**: Only commit offsets where all prior offsets are complete
4. **Fail-safe**: Failed messages block commits, allowing reprocessing on restart

**Architecture:**
```
Kafka Consumer (single-threaded poll)
     ↓
Message Dispatcher
     ↓
Worker Pool (N goroutines) ← CONCURRENT PROCESSING
     ↓
Result Processor
     ↓
Offset Tracker (per partition) ← GAP DETECTION
     ↓
Commit Manager ← ORDERED COMMITS
```

## Features

### Core Features
- ✅ **Concurrent processing**: Configurable worker pool (e.g., 100 workers)
- ✅ **At-least-once guarantees**: No message loss due to ordered commits
- ✅ **Ordered commits**: Only commits contiguous offsets (gap detection)
- ✅ **Backpressure**: Bounded channels prevent memory exhaustion
- ✅ **Partition-aware**: Independent tracking per partition

### Reliability Features
- ✅ **Error tracking**: Threshold-based halt when errors exceed limit
- ✅ **Retry with backoff**: Exponential backoff for transient failures
- ✅ **Graceful shutdown**: Waits for inflight messages before stopping
- ✅ **Rebalance handling**: Properly handles partition assignment changes
- ✅ **Memory-safe**: Cleans up old offset entries after commit

### Developer Experience
- ✅ **Simple API**: Looks like standard Kafka consumer
- ✅ **Metrics**: Prometheus metrics for monitoring
- ✅ **Structured logging**: Integration with zap logger
- ✅ **Context-aware**: Proper context cancellation support

## Quick Start

### Installation

```bash
go get github.com/vlab-research/fly/burrow
```

### Basic Usage

```go
package main

import (
    "context"
    "log"

    "github.com/confluentinc/confluent-kafka-go/v2/kafka"
    "github.com/vlab-research/fly/burrow"
    "go.uber.org/zap"
)

func main() {
    // Create standard Kafka consumer
    consumer, err := kafka.NewConsumer(&kafka.ConfigMap{
        "bootstrap.servers": "localhost:9092",
        "group.id":          "my-group",
        "auto.offset.reset": "earliest",
        "enable.auto.commit": false,  // CRITICAL: Burrow handles commits
    })
    if err != nil {
        log.Fatal(err)
    }
    defer consumer.Close()

    // Subscribe to topics
    consumer.SubscribeTopics([]string{"my-topic"}, nil)

    // Create logger
    logger, _ := zap.NewProduction()

    // Create Burrow pool with 100 workers
    pool := burrow.NewPool(consumer, burrow.Config{
        NumWorkers:     100,
        CommitInterval: 5 * time.Second,
        MaxErrors:      10,
        Logger:         logger,
    })

    // Define your message processing function
    processFunc := func(ctx context.Context, msg *kafka.Message) error {
        // Your I/O-heavy logic here
        return processMessage(msg)
    }

    // Run the pool (blocks until context cancelled)
    ctx := context.Background()
    if err := pool.Run(ctx, processFunc); err != nil {
        log.Fatal(err)
    }
}

func processMessage(msg *kafka.Message) error {
    // Example: API call, database query, etc.
    // This runs concurrently across 100 workers!
    return nil
}
```

## Documentation Index

- [Architecture Design](./docs/ARCHITECTURE.md) - Detailed system architecture
- [Implementation Plan](./docs/IMPLEMENTATION_PLAN.md) - Step-by-step implementation guide
- [API Documentation](./docs/API.md) - Complete API reference
- [Design Decisions](./docs/DESIGN_DECISIONS.md) - Why we made certain choices
- [Testing Strategy](./docs/TESTING.md) - How to test Burrow
- [Performance Tuning](./docs/PERFORMANCE.md) - Optimization guide
- [Comparison with Eddies](./docs/EDDIES_COMPARISON.md) - How Burrow relates to Eddies

## Project Structure

```
burrow/
├── README.md                 # This file
├── docs/                     # Documentation
│   ├── ARCHITECTURE.md
│   ├── IMPLEMENTATION_PLAN.md
│   ├── API.md
│   ├── DESIGN_DECISIONS.md
│   ├── TESTING.md
│   ├── PERFORMANCE.md
│   └── EDDIES_COMPARISON.md
├── pool.go                   # Main Pool API
├── consumer.go               # ConcurrentConsumer (orchestrator)
├── worker.go                 # Worker implementation
├── tracker.go                # OffsetTracker (gap detection)
├── commit.go                 # CommitManager
├── errors.go                 # Error tracking
├── types.go                  # Shared types
├── config.go                 # Configuration
├── metrics.go                # Prometheus metrics
├── examples/                 # Usage examples
│   ├── simple/
│   ├── with-retry/
│   └── with-metrics/
└── tests/                    # Test files
    ├── pool_test.go
    ├── tracker_test.go
    ├── integration_test.go
    └── testdata/
```

## Key Concepts

### 1. Offset Tracker

Each Kafka partition gets its own offset tracker that maintains a map of which offsets have been processed:

```go
type OffsetTracker struct {
    partition       int32
    processedMap    map[int64]bool    // offset -> completed?
    lastCommitted   int64
    highWatermark   int64
}
```

### 2. Gap Detection

The tracker computes the "committable offset" by finding the highest contiguous offset:

```go
func (ot *OffsetTracker) GetCommittableOffset() int64 {
    // Returns highest offset where ALL prior offsets are complete
    for offset := lastCommitted + 1; offset <= highWatermark; offset++ {
        if !processedMap[offset] {
            break  // Gap! Can't commit beyond this
        }
        committable = offset
    }
    return committable
}
```

### 3. Worker Pool

Fixed number of goroutines that pull jobs from a buffered channel:

```go
type Worker struct {
    id           int
    jobsChan     <-chan *Job
    resultsChan  chan<- *Result
}
```

The bounded channel provides natural backpressure - if workers can't keep up, the consumer blocks.

### 4. Periodic Commits

Burrow commits offsets periodically (e.g., every 5 seconds) rather than on every message, using **synchronous commits** for safety:

```go
func (cm *CommitManager) commitLoop(ctx context.Context) {
    ticker := time.NewTicker(commitInterval)
    for {
        select {
        case <-ticker.C:
            cm.tryCommit()  // Only commits contiguous offsets
        }
    }
}
```

## Use Cases

### Perfect For ✅
- I/O-heavy operations (API calls, database queries, file operations)
- High latency variance (some messages fast, others slow)
- At-least-once semantics acceptable (idempotent operations)
- Message loss is unacceptable

### Not Ideal For ❌
- CPU-bound operations (use more partitions instead)
- Strict exactly-once requirements (use Kafka transactions)
- Sub-millisecond latency critical (adds coordination overhead)

## Trade-offs

### Advantages
- **High throughput**: Concurrent processing of I/O operations
- **No message loss**: Ordered commits ensure at-least-once
- **Fail-safe**: Failed messages block commits (reprocessed on restart)
- **Backpressure**: Bounded queue prevents memory issues
- **Simple API**: Drop-in replacement for standard consumer

### Disadvantages
- **Head-of-line blocking**: One slow message blocks commits for all after it
- **Memory overhead**: Must track all inflight offsets
- **Duplicate processing**: On restart after failure, some messages may be processed twice
- **Complexity**: More complex than single-threaded consumer

## Inspiration

Burrow is inspired by:
- [Eddies](https://github.com/Relink/eddies) - Node.js concurrent stream workers
- [Confluent Parallel Consumer](https://github.com/confluentinc/parallel-consumer) - Java parallel consumer
- [Sift Engineering's approach](https://engineering.sift.com/concurrency-and-at-least-once-semantics-with-the-new-kafka-consumer/) - Concurrent Kafka consumer pattern

## License

MIT License - See LICENSE file for details

## Contributing

Contributions welcome! Please see CONTRIBUTING.md for guidelines.

## Status

**Current Status**: Design phase - Implementation pending

**Roadmap**:
- Phase 1: Core implementation (Weeks 1-2)
- Phase 2: Testing & reliability (Week 3)
- Phase 3: Production hardening (Week 4)
- Phase 4: Documentation & examples (Week 5)

---

**Built with ❤️ for the Kafka + Go community**
