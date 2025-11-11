# Burrow

> Concurrent Kafka consumer library for Go with at-least-once delivery guarantees

[![Go Version](https://img.shields.io/badge/go-%3E%3D1.25-blue)](https://go.dev/)
[![License](https://img.shields.io/badge/license-MIT-green)](LICENSE)

## Overview

**Burrow** is a Go library that enables concurrent processing of Kafka messages while maintaining at-least-once delivery guarantees. It solves the challenge of processing I/O-heavy operations in parallel without losing messages due to out-of-order completion.

### The Problem

```
Sequential Processing:  Slow ❌
Message 1 → [50ms] → Message 2 → [50ms] → Message 3 → [50ms]
Throughput: 20 msgs/sec

Naive Parallel: Fast but loses messages ❌
Messages [1,2,3,4,5] → Process concurrently → Complete [1,3,5,2,4]
Commit offset 5 → Lose messages 2,3,4 on restart!

Burrow: Fast AND safe ✅
Messages [1,2,3,4,5] → Process concurrently → Gap detection → Commit offset 1
→ Restart → Reprocess [2,3,4,5]
```

### The Solution

Burrow provides:
- **100x throughput improvement** for I/O-bound operations
- **At-least-once delivery** through ordered offset commits
- **Gap detection** to prevent message loss
- **Simple API** that works like standard Kafka consumer

## Installation

```bash
go get github.com/vlab-research/burrow
```

## Quick Start

```go
package main

import (
    "context"
    "log"
    "time"

    "github.com/confluentinc/confluent-kafka-go/v2/kafka"
    "github.com/vlab-research/burrow"
    "go.uber.org/zap"
)

func main() {
    // 1. Create standard Kafka consumer
    consumer, err := kafka.NewConsumer(&kafka.ConfigMap{
        "bootstrap.servers":  "localhost:9092",
        "group.id":           "my-group",
        "auto.offset.reset":  "earliest",
        "enable.auto.commit": false, // IMPORTANT: Burrow handles commits
    })
    if err != nil {
        log.Fatal(err)
    }
    defer consumer.Close()

    // 2. Subscribe to topics
    consumer.SubscribeTopics([]string{"my-topic"}, nil)

    // 3. Create Burrow pool
    logger, _ := zap.NewProduction()
    config := burrow.DefaultConfig(logger)
    config.NumWorkers = 100 // 100 concurrent workers

    pool, err := burrow.NewPool(consumer, config)
    if err != nil {
        log.Fatal(err)
    }

    // 4. Define processing function
    processFunc := func(ctx context.Context, msg *kafka.Message) error {
        // Your I/O-heavy logic here (API calls, database queries, etc.)
        time.Sleep(50 * time.Millisecond) // Simulated I/O
        log.Printf("Processed: %s", string(msg.Value))
        return nil
    }

    // 5. Run the pool
    ctx := context.Background()
    if err := pool.Run(ctx, processFunc); err != nil {
        log.Fatal(err)
    }
}
```

## Features

### Core Capabilities

- ✅ **Concurrent Processing** - Configurable worker pool (1 to 1000+ workers)
- ✅ **At-Least-Once Delivery** - Gap detection ensures no message loss
- ✅ **Ordered Commits** - Only commits contiguous offsets
- ✅ **Backpressure** - Bounded channels prevent memory exhaustion
- ✅ **Partition-Aware** - Independent tracking per partition

### Reliability

- ✅ **Error Threshold** - Automatic halt when errors exceed limit
- ✅ **Retry with Backoff** - Exponential backoff for transient failures
- ✅ **Graceful Shutdown** - Waits for inflight messages
- ✅ **Rebalance Safe** - Proper partition reassignment handling
- ✅ **Memory Safe** - Automatic cleanup after commits

### Observability

- ✅ **Metrics** - `GetStats()` for monitoring
- ✅ **Structured Logging** - zap logger integration
- ✅ **Context Support** - Standard Go cancellation

## Configuration

### Basic Configuration

```go
config := burrow.DefaultConfig(logger)
```

Default values:
- `NumWorkers`: 10
- `JobQueueSize`: 1000
- `ResultQueueSize`: 1000
- `CommitInterval`: 5 seconds
- `CommitBatchSize`: 1000 messages
- `MaxConsecutiveErrors`: 5
- `MaxRetries`: 3
- `RetryBackoffBase`: 1 second

### Custom Configuration

```go
config := burrow.Config{
    NumWorkers:           100,             // 100 concurrent workers
    JobQueueSize:         10000,           // Larger buffer
    ResultQueueSize:      10000,
    CommitInterval:       10 * time.Second, // Commit every 10s
    CommitBatchSize:      5000,            // Or every 5000 messages
    MaxConsecutiveErrors: 10,              // Tolerate more errors
    MaxRetries:           5,               // More retries
    RetryBackoffBase:     2 * time.Second, // Slower backoff
    Logger:               logger,
    EnableMetrics:        true,
}

pool, err := burrow.NewPool(consumer, config)
```

### Tuning Workers

Choose worker count based on workload:

```go
// I/O-bound work (API calls, database queries)
config.NumWorkers = 100  // High concurrency

// CPU-bound work
config.NumWorkers = runtime.NumCPU()  // Match CPU cores

// Mixed workload
config.NumWorkers = runtime.NumCPU() * 2  // Start here and measure
```

## Monitoring

### Get Runtime Statistics

```go
stats := pool.GetStats()
fmt.Printf("Processed: %d\n", stats.MessagesProcessed)
fmt.Printf("Failed: %d\n", stats.MessagesFailed)
fmt.Printf("Committed: %d\n", stats.OffsetsCommitted)
fmt.Printf("Workers: %d\n", stats.WorkersActive)
fmt.Printf("Queued: %d\n", stats.JobsQueued)
```

### Key Metrics to Monitor

- `MessagesProcessed` - Throughput indicator
- `MessagesFailed` - Error rate
- `OffsetsCommitted` - Commit frequency
- `JobsQueued` - Backpressure indicator (should be < QueueSize)

Alert when:
- `MessagesFailed` is high
- `JobsQueued` approaches `JobQueueSize` (backpressure)
- `MessagesProcessed` drops unexpectedly

## Error Handling

### Retriable vs. Permanent Errors

```go
processFunc := func(ctx context.Context, msg *kafka.Message) error {
    result, err := callExternalAPI(msg)
    if err != nil {
        // Network errors are retriable (automatic retry)
        if isNetworkError(err) {
            return err  // Burrow will retry with backoff
        }

        // Business logic errors are permanent (no retry)
        return fmt.Errorf("permanent error: %w", err)
    }

    return saveToDatabase(result)
}
```

Burrow automatically retries failed messages with exponential backoff (1s, 2s, 4s, 8s, ..., max 60s).

### Error Threshold

If `MaxConsecutiveErrors` is exceeded, Burrow halts processing to prevent cascading failures:

```go
config.MaxConsecutiveErrors = 5  // Halt after 5 consecutive errors
```

Successful processing resets the counter.

## Graceful Shutdown

```go
ctx, cancel := context.WithCancel(context.Background())

// Handle signals
go func() {
    sigch := make(chan os.Signal, 1)
    signal.Notify(sigch, os.Interrupt, syscall.SIGTERM)
    <-sigch
    cancel()  // Trigger graceful shutdown
}()

// Run pool (blocks until context cancelled)
if err := pool.Run(ctx, processFunc); err != nil {
    log.Fatal(err)
}

// Pool automatically:
// 1. Stops accepting new messages
// 2. Waits for inflight messages to complete
// 3. Commits final offsets
// 4. Cleans up resources
```

## Examples

See the [examples/](examples/) directory for complete examples:

- [`simple/`](examples/simple/) - Basic usage
- Coming soon: `with-retry/` - Custom retry logic
- Coming soon: `with-metrics/` - Prometheus metrics

## How It Works

### Gap Detection

Burrow tracks which offsets are complete and only commits contiguous offsets:

```
Consumed:  [0, 1, 2, 3, 4, 5]
Completed: [0, 2, 4, 5]         ← Processing done
Gaps:      [1, 3]               ← Still processing or failed
Committable: 0                  ← Can only commit up to 0

After 1 completes:
Completed: [0, 1, 2, 4, 5]
Committable: 2                  ← Now can commit 0,1,2

After 3 completes:
Completed: [0, 1, 2, 3, 4, 5]
Committable: 5                  ← All done!
```

This ensures **at-least-once delivery**: if your app crashes, you restart from the last committed offset and reprocess any gaps.

## Performance

### Throughput Improvement

```
Sequential:  20 msgs/sec (50ms per message)
Burrow:      2000 msgs/sec (100 workers)
Improvement: 100x
```

Actual performance depends on your I/O operations.

### Overhead

- **Memory**: O(W) where W = inflight messages (~1KB per message)
- **Latency**: ~1ms coordination overhead per message
- **Commit**: ~5-10ms (synchronous Kafka commit)

## Use Cases

### Perfect For ✅

- API calls to external services
- Database queries with high latency
- File I/O operations
- Image/video processing
- Any I/O-bound operation with variable latency

### Not Ideal For ❌

- CPU-bound operations (use more partitions instead)
- Exactly-once semantics required (use Kafka transactions)
- Sub-millisecond latency critical (coordination adds overhead)

## Architecture

For detailed architecture and design decisions, see [DESIGN.md](DESIGN.md).

```
Kafka → Poll → Dispatch → [Worker Pool] → Results → Gap Detection → Commit
                              ↓
                         100 goroutines
                         process concurrently
```

## Testing

```bash
# Run all tests
go test ./tests/...

# With race detector
go test -race ./tests/...

# Specific test
go test -run TestIntegration_GapDetection ./tests/
```

## Contributing

Contributions welcome! Please:
1. Fork the repository
2. Create a feature branch
3. Add tests for new functionality
4. Ensure all tests pass with `-race`
5. Submit a pull request

## License

MIT License - See [LICENSE](LICENSE) file for details.

## Inspiration

Burrow is inspired by:
- [Eddies](https://github.com/Relink/eddies) - Worker pool pattern
- [Confluent Parallel Consumer](https://github.com/confluentinc/parallel-consumer) - Gap detection approach
- [Sift Engineering](https://engineering.sift.com/concurrency-and-at-least-once-semantics-with-the-new-kafka-consumer/) - At-least-once semantics

## Support

- **Documentation**: See [DESIGN.md](DESIGN.md) for architecture details
- **Issues**: Report bugs on GitHub Issues
- **Questions**: Open a discussion on GitHub Discussions

---

**Built with ❤️ for the Kafka + Go community**
