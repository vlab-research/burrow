# Getting Started with Burrow

## What is Burrow?

Burrow is a Go library for building concurrent Kafka consumers that:
- ✅ Process messages in parallel (e.g., 100 workers)
- ✅ Maintain at-least-once delivery guarantees
- ✅ Commit offsets in order (no message loss)
- ✅ Handle failures gracefully

**Perfect for**: I/O-heavy operations like API calls, database queries, or file processing.

## Installation

```bash
go get github.com/vlab-research/fly/burrow
```

## 5-Minute Quick Start

### Step 1: Create a Standard Kafka Consumer

```go
import (
    "github.com/confluentinc/confluent-kafka-go/v2/kafka"
    "github.com/vlab-research/fly/burrow"
    "go.uber.org/zap"
)

// Create standard Kafka consumer
consumer, err := kafka.NewConsumer(&kafka.ConfigMap{
    "bootstrap.servers": "localhost:9092",
    "group.id":          "my-consumer-group",
    "auto.offset.reset": "earliest",
    "enable.auto.commit": false,  // CRITICAL: Let Burrow handle commits
})
if err != nil {
    log.Fatal(err)
}
defer consumer.Close()

// Subscribe to topics
consumer.SubscribeTopics([]string{"my-topic"}, nil)
```

### Step 2: Create Burrow Pool

```go
// Create logger
logger, _ := zap.NewProduction()

// Create Burrow pool with 100 workers
pool, err := burrow.NewPool(consumer, burrow.Config{
    NumWorkers:     100,                  // Process 100 messages concurrently
    CommitInterval: 5 * time.Second,      // Commit every 5 seconds
    MaxErrors:      10,                   // Halt after 10 consecutive errors
    Logger:         logger,
})
if err != nil {
    log.Fatal(err)
}
```

### Step 3: Define Processing Function

```go
// Your message processing logic
processFunc := func(ctx context.Context, msg *kafka.Message) error {
    // Parse message
    var data MyData
    if err := json.Unmarshal(msg.Value, &data); err != nil {
        return err  // Will be retried
    }

    // Do your I/O-heavy work here
    // Examples: API calls, database queries, file operations
    if err := myDatabase.Insert(data); err != nil {
        return err  // Will be retried
    }

    // Success!
    return nil
}
```

### Step 4: Run the Pool

```go
// Create context for graceful shutdown
ctx, cancel := context.WithCancel(context.Background())
defer cancel()

// Handle shutdown signals
sigChan := make(chan os.Signal, 1)
signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
go func() {
    <-sigChan
    log.Println("Shutting down...")
    cancel()
}()

// Run pool (blocks until context cancelled)
if err := pool.Run(ctx, processFunc); err != nil && err != context.Canceled {
    log.Fatal(err)
}

log.Println("Shutdown complete")
```

## Complete Example

```go
package main

import (
    "context"
    "encoding/json"
    "log"
    "os"
    "os/signal"
    "syscall"
    "time"

    "github.com/confluentinc/confluent-kafka-go/v2/kafka"
    "github.com/vlab-research/fly/burrow"
    "go.uber.org/zap"
)

type MyData struct {
    ID   string `json:"id"`
    Name string `json:"name"`
}

func main() {
    // 1. Create Kafka consumer
    consumer, err := kafka.NewConsumer(&kafka.ConfigMap{
        "bootstrap.servers":  "localhost:9092",
        "group.id":           "my-group",
        "auto.offset.reset":  "earliest",
        "enable.auto.commit": false,
    })
    if err != nil {
        log.Fatal(err)
    }
    defer consumer.Close()

    consumer.SubscribeTopics([]string{"my-topic"}, nil)

    // 2. Create logger
    logger, _ := zap.NewProduction()
    defer logger.Sync()

    // 3. Create Burrow pool
    config := burrow.DefaultConfig(logger)
    config.NumWorkers = 100  // 100 concurrent workers

    pool, err := burrow.NewPool(consumer, config)
    if err != nil {
        log.Fatal(err)
    }

    // 4. Define processing function
    processFunc := func(ctx context.Context, msg *kafka.Message) error {
        var data MyData
        if err := json.Unmarshal(msg.Value, &data); err != nil {
            return err
        }

        // Simulate I/O operation
        time.Sleep(50 * time.Millisecond)

        logger.Info("processed message",
            zap.String("id", data.ID),
            zap.String("name", data.Name))

        return nil
    }

    // 5. Setup graceful shutdown
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
    go func() {
        <-sigChan
        logger.Info("shutdown signal received")
        cancel()
    }()

    // 6. Run pool
    logger.Info("starting pool")
    if err := pool.Run(ctx, processFunc); err != nil && err != context.Canceled {
        log.Fatal(err)
    }

    logger.Info("shutdown complete")
}
```

## Key Concepts

### 1. At-Least-Once Guarantees

Burrow ensures **no message loss** by only committing offsets when all prior messages are processed:

```
Messages: [0, 1, 2, 3, 4, 5]
Processed: [0, 1, 2, 4, 5]  ← 3 failed!
Committed: up to offset 2    ← Safe (no gap)

On restart: Message 3 will be reprocessed
```

### 2. Concurrent Processing

100 workers process messages in parallel:

```
Without Burrow: 100 messages × 50ms = 5 seconds
With Burrow (100 workers): 100 messages / 100 workers = 50ms
```

**Throughput increase: 100x!**

### 3. Ordered Commits

Even though processing is concurrent, commits are ordered:

```
Poll order:    [msg0, msg1, msg2, msg3]
Process order: [msg0, msg2, msg3, msg1]  ← Out of order!
Commit order:  [msg0, msg1, msg2, msg3]  ← In order!
```

## Configuration Options

```go
config := burrow.Config{
    // Worker pool
    NumWorkers:      100,                      // Number of concurrent workers
    JobQueueSize:    1000,                     // Buffer size (backpressure)

    // Commit behavior
    CommitInterval:  5 * time.Second,          // Commit every 5 seconds
    CommitBatchSize: 1000,                     // OR every 1000 messages

    // Error handling
    MaxConsecutiveErrors: 10,                  // Halt after 10 errors in a row
    MaxRetries:           3,                   // Retry each message 3 times
    RetryBackoffBase:     100 * time.Millisecond,  // Exponential backoff

    // Required
    Logger: logger,                            // Zap logger
}
```

## Common Patterns

### Pattern 1: Database Inserts

```go
processFunc := func(ctx context.Context, msg *kafka.Message) error {
    var record Record
    json.Unmarshal(msg.Value, &record)

    // Use upsert to handle duplicates
    return db.Upsert(ctx, record)
}
```

### Pattern 2: API Calls

```go
processFunc := func(ctx context.Context, msg *kafka.Message) error {
    // Add timeout
    ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
    defer cancel()

    // Make API call
    resp, err := httpClient.Post(ctx, url, msg.Value)
    if err != nil {
        return err  // Will retry if transient
    }

    return handleResponse(resp)
}
```

### Pattern 3: Batch Processing

```go
var batch []Message
var mu sync.Mutex

processFunc := func(ctx context.Context, msg *kafka.Message) error {
    mu.Lock()
    batch = append(batch, msg)
    shouldFlush := len(batch) >= 100
    mu.Unlock()

    if shouldFlush {
        mu.Lock()
        currentBatch := batch
        batch = nil
        mu.Unlock()

        return db.BatchInsert(currentBatch)
    }

    return nil
}
```

## Troubleshooting

### Problem: High Memory Usage

**Cause**: Large offset gaps (many failed messages)

**Solution**:
```go
config.MaxConsecutiveErrors = 5  // Halt sooner
config.MaxRetries = 2            // Fewer retries
```

### Problem: Slow Processing

**Cause**: Not enough workers

**Solution**:
```go
config.NumWorkers = 200  // More workers
```

### Problem: Messages Processing Twice

**Cause**: At-least-once semantics (expected behavior)

**Solution**: Make your ProcessFunc idempotent:
```go
processFunc := func(ctx context.Context, msg *kafka.Message) error {
    id := extractID(msg)

    // Check if already processed
    if db.Exists(id) {
        return nil  // Skip
    }

    // Process with unique constraint
    return db.InsertWithUniqueConstraint(id, data)
}
```

## Next Steps

1. **Read the [Architecture](./docs/ARCHITECTURE.md)** to understand how Burrow works
2. **Check [API Documentation](./docs/API.md)** for complete reference
3. **See [Implementation Plan](./docs/IMPLEMENTATION_PLAN.md)** if you want to contribute
4. **Review [Eddies Comparison](./docs/EDDIES_COMPARISON.md)** to understand the inspiration

## FAQ

**Q: How does Burrow compare to using multiple consumer instances?**

A: Multiple instances are limited by partition count. Burrow allows more workers per partition. If you have 3 partitions, you can have 100 workers per partition = 300 total workers!

**Q: What if I need exactly-once semantics?**

A: Burrow provides at-least-once. For exactly-once, use Kafka transactions (not yet supported in Burrow).

**Q: Can I use Burrow with non-I/O-bound work?**

A: Not recommended. For CPU-bound work, add more partitions instead.

**Q: How do I monitor Burrow?**

A: Enable metrics and use Prometheus:
```go
config.EnableMetrics = true

// Expose metrics endpoint
http.Handle("/metrics", promhttp.Handler())
go http.ListenAndServe(":9090", nil)
```

**Q: What happens during partition rebalancing?**

A: Burrow automatically:
1. Waits for inflight messages to complete
2. Commits final offsets
3. Cleans up partition trackers

No special handling needed!

## Support

- **Documentation**: See `docs/` folder
- **Issues**: [GitHub Issues](https://github.com/vlab-research/fly/burrow/issues)
- **Examples**: See `examples/` folder

## License

MIT License

---

**Happy concurrent processing!** 🚀
