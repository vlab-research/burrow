# Burrow API Documentation

## Quick Reference

```go
import "github.com/vlab-research/fly/burrow"

// Create pool
pool, err := burrow.NewPool(consumer, burrow.DefaultConfig(logger))

// Define processing function
processFunc := func(ctx context.Context, msg *kafka.Message) error {
    return doSomething(msg)
}

// Run
err = pool.Run(ctx, processFunc)
```

## Core Types

### Pool

Main entry point for Burrow.

```go
type Pool struct {
    // Private fields
}
```

#### func NewPool

```go
func NewPool(consumer *kafka.Consumer, config Config) (*Pool, error)
```

Creates a new Burrow pool.

**Parameters**:
- `consumer`: Standard Kafka consumer (must have `enable.auto.commit = false`)
- `config`: Configuration (use `DefaultConfig()` for defaults)

**Returns**:
- `*Pool`: Configured pool instance
- `error`: Validation error if config is invalid

**Example**:
```go
consumer, _ := kafka.NewConsumer(&kafka.ConfigMap{
    "bootstrap.servers": "localhost:9092",
    "group.id":          "my-group",
    "enable.auto.commit": false,  // REQUIRED
})

logger, _ := zap.NewProduction()
pool, err := burrow.NewPool(consumer, burrow.DefaultConfig(logger))
if err != nil {
    log.Fatal(err)
}
```

#### func (*Pool) Run

```go
func (p *Pool) Run(ctx context.Context, processFunc ProcessFunc) error
```

Starts the pool and processes messages until context is cancelled.

**Parameters**:
- `ctx`: Context for cancellation
- `processFunc`: User-defined function to process each message

**Returns**:
- `error`: Context cancellation or fatal error

**Behavior**:
- Blocks until context is cancelled
- Processes messages concurrently
- Commits offsets periodically
- Gracefully shuts down on context cancellation

**Example**:
```go
ctx, cancel := context.WithCancel(context.Background())
defer cancel()

processFunc := func(ctx context.Context, msg *kafka.Message) error {
    // Process message
    return nil
}

err := pool.Run(ctx, processFunc)
if err != nil && err != context.Canceled {
    log.Fatal(err)
}
```

#### func (*Pool) GetStats

```go
func (p *Pool) GetStats() Stats
```

Returns runtime statistics.

**Returns**:
- `Stats`: Current pool statistics

**Example**:
```go
stats := pool.GetStats()
fmt.Printf("Processed: %d, Failed: %d\n",
    stats.MessagesProcessed,
    stats.MessagesFailed)
```

### Config

Configuration for the pool.

```go
type Config struct {
    NumWorkers           int
    JobQueueSize         int
    ResultQueueSize      int
    CommitInterval       time.Duration
    CommitBatchSize      int
    MaxConsecutiveErrors int
    MaxRetries           int
    RetryBackoffBase     time.Duration
    Logger               *zap.Logger
    EnableMetrics        bool
    EnableOrderedProcessing bool
    ShutdownTimeout      time.Duration
}
```

#### Fields

**NumWorkers** `int`
- Number of concurrent workers
- Default: 10
- Recommended: 2-4 × CPU cores for I/O-bound work

**JobQueueSize** `int`
- Size of job queue buffer
- Default: 1000
- Provides backpressure when workers can't keep up

**ResultQueueSize** `int`
- Size of result queue buffer
- Default: 1000
- Should be >= JobQueueSize

**CommitInterval** `time.Duration`
- How often to commit offsets
- Default: 5 seconds
- Trade-off: Shorter = less duplicate work on restart, longer = less Kafka overhead

**CommitBatchSize** `int`
- Max messages before forcing commit
- Default: 1000
- Commits when EITHER interval OR batch size reached

**MaxConsecutiveErrors** `int`
- Max consecutive errors before halting
- Default: 10
- Prevents runaway failures

**MaxRetries** `int`
- Max retry attempts per message
- Default: 3
- After this, message is permanently failed

**RetryBackoffBase** `time.Duration`
- Base duration for exponential backoff
- Default: 100ms
- Formula: `base × 2^attempt`

**Logger** `*zap.Logger`
- Logger instance (REQUIRED)
- Used for all logging

**EnableMetrics** `bool`
- Enable Prometheus metrics
- Default: false
- Set to true to expose metrics

**EnableOrderedProcessing** `bool`
- Ensure per-partition ordering
- Default: false
- Reduces throughput but maintains order

**ShutdownTimeout** `time.Duration`
- Graceful shutdown timeout
- Default: 30 seconds
- How long to wait for inflight messages

#### func DefaultConfig

```go
func DefaultConfig(logger *zap.Logger) Config
```

Returns configuration with sensible defaults.

**Example**:
```go
logger, _ := zap.NewProduction()
config := burrow.DefaultConfig(logger)
config.NumWorkers = 100  // Override default
pool, _ := burrow.NewPool(consumer, config)
```

#### func (Config) Validate

```go
func (c Config) Validate() error
```

Validates configuration.

**Returns**:
- `error`: Validation error if config is invalid

**Validates**:
- NumWorkers > 0
- Logger != nil
- CommitInterval > 0
- Other constraints

### ProcessFunc

User-defined function for processing messages.

```go
type ProcessFunc func(context.Context, *kafka.Message) error
```

**Parameters**:
- `ctx`: Context (check for cancellation)
- `msg`: Kafka message to process

**Returns**:
- `error`: nil for success, error for failure

**Behavior**:
- Called concurrently by multiple workers
- Should be thread-safe
- Should respect context cancellation
- Return error for retry (if retriable)

**Example**:
```go
processFunc := func(ctx context.Context, msg *kafka.Message) error {
    // Parse message
    var data MyData
    if err := json.Unmarshal(msg.Value, &data); err != nil {
        return err  // Will retry
    }

    // Check context
    select {
    case <-ctx.Done():
        return ctx.Err()
    default:
    }

    // Process (e.g., database insert)
    if err := db.Insert(ctx, data); err != nil {
        return err  // Will retry if transient
    }

    return nil  // Success
}
```

### Stats

Runtime statistics.

```go
type Stats struct {
    MessagesProcessed int64
    MessagesFailed    int64
    OffsetsCommitted  int64
    WorkersActive     int
    JobsQueued        int
}
```

**Fields**:
- `MessagesProcessed`: Total messages successfully processed
- `MessagesFailed`: Total messages that permanently failed
- `OffsetsCommitted`: Total offsets committed to Kafka
- `WorkersActive`: Number of workers currently processing
- `JobsQueued`: Number of jobs in queue

## Advanced Usage

### Custom Configuration

```go
logger, _ := zap.NewProduction()

config := burrow.Config{
    NumWorkers:           100,              // 100 concurrent workers
    JobQueueSize:         5000,             // Larger buffer
    ResultQueueSize:      5000,
    CommitInterval:       10 * time.Second, // Commit every 10s
    CommitBatchSize:      5000,             // Or every 5000 messages
    MaxConsecutiveErrors: 50,               // Allow more errors
    MaxRetries:           5,                // More retries
    RetryBackoffBase:     200 * time.Millisecond,
    Logger:               logger,
    EnableMetrics:        true,             // Enable metrics
    ShutdownTimeout:      60 * time.Second,
}

pool, _ := burrow.NewPool(consumer, config)
```

### Context-Aware Processing

```go
processFunc := func(ctx context.Context, msg *kafka.Message) error {
    // Create child context with timeout
    ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
    defer cancel()

    // Make API call with context
    resp, err := httpClient.Get(ctx, url)
    if err != nil {
        if errors.Is(err, context.DeadlineExceeded) {
            return fmt.Errorf("timeout: %w", err)  // Retriable
        }
        return err
    }

    return processResponse(resp)
}
```

### Metrics Integration

```go
import (
    "github.com/prometheus/client_golang/prometheus/promhttp"
    "net/http"
)

// Enable metrics in config
config := burrow.DefaultConfig(logger)
config.EnableMetrics = true

// Expose metrics endpoint
http.Handle("/metrics", promhttp.Handler())
go http.ListenAndServe(":9090", nil)

// Run pool
pool.Run(ctx, processFunc)
```

### Graceful Shutdown

```go
ctx, cancel := context.WithCancel(context.Background())

// Handle signals
sigChan := make(chan os.Signal, 1)
signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

go func() {
    <-sigChan
    logger.Info("shutdown signal received")
    cancel()  // Cancel context
}()

// Run pool (blocks)
err := pool.Run(ctx, processFunc)
logger.Info("pool stopped", zap.Error(err))
```

### Error Handling

```go
processFunc := func(ctx context.Context, msg *kafka.Message) error {
    // Permanent error (don't retry)
    if !isValid(msg) {
        // Log and return non-retriable error
        logger.Error("invalid message", zap.ByteString("value", msg.Value))
        return &NonRetriableError{Msg: "invalid format"}
    }

    // Transient error (will retry)
    if err := apiCall(); err != nil {
        if isNetworkError(err) {
            return err  // Retry
        }
        return &NonRetriableError{Msg: err.Error()}  // Don't retry
    }

    return nil
}

// Custom error type
type NonRetriableError struct {
    Msg string
}

func (e *NonRetriableError) Error() string {
    return e.Msg
}

// Update isRetriable to check error type
func isRetriable(err error) bool {
    var nonRetriable *NonRetriableError
    return !errors.As(err, &nonRetriable)
}
```

## Best Practices

### 1. Always Set enable.auto.commit = false

```go
consumer, _ := kafka.NewConsumer(&kafka.ConfigMap{
    "bootstrap.servers":  "localhost:9092",
    "group.id":           "my-group",
    "enable.auto.commit": false,  // REQUIRED for Burrow
})
```

### 2. Make ProcessFunc Idempotent

Since messages may be processed multiple times (at-least-once), ensure your function is idempotent:

```go
processFunc := func(ctx context.Context, msg *kafka.Message) error {
    // Use unique ID to prevent duplicates
    id := extractID(msg)

    // Check if already processed
    if db.Exists(id) {
        return nil  // Already processed, skip
    }

    // Process and store with unique constraint
    return db.InsertWithUniqueConstraint(id, data)
}
```

### 3. Use Structured Logging

```go
processFunc := func(ctx context.Context, msg *kafka.Message) error {
    logger.Info("processing message",
        zap.Int32("partition", msg.TopicPartition.Partition),
        zap.Int64("offset", int64(msg.TopicPartition.Offset)),
        zap.Int("size", len(msg.Value)))

    // Process...

    return nil
}
```

### 4. Monitor Metrics

```go
// Track custom metrics
var processingLatency = promauto.NewHistogram(prometheus.HistogramOpts{
    Name: "app_processing_latency_seconds",
    Help: "Processing latency",
})

processFunc := func(ctx context.Context, msg *kafka.Message) error {
    start := time.Now()
    defer func() {
        processingLatency.Observe(time.Since(start).Seconds())
    }()

    // Process...
    return nil
}
```

### 5. Handle Partition Rebalancing

Burrow handles this automatically, but you can add cleanup logic:

```go
// No special handling needed - Burrow waits for inflight messages
// before releasing partitions
```

## Troubleshooting

### High Memory Usage

**Cause**: Large offset gaps (many failed messages)

**Solution**:
- Fix message processing errors
- Reduce MaxRetries
- Lower MaxConsecutiveErrors (halt sooner)

### Slow Commits

**Cause**: Head-of-line blocking (slow messages)

**Solution**:
- Optimize ProcessFunc
- Add timeout to slow operations
- Increase CommitBatchSize

### Worker Starvation

**Cause**: Not enough workers for message volume

**Solution**:
- Increase NumWorkers
- Optimize ProcessFunc
- Add more consumer instances

## API Reference Summary

| Function | Description |
|----------|-------------|
| `NewPool(consumer, config)` | Create new pool |
| `Pool.Run(ctx, processFunc)` | Start processing |
| `Pool.GetStats()` | Get statistics |
| `DefaultConfig(logger)` | Get default config |
| `Config.Validate()` | Validate config |

| Type | Description |
|------|-------------|
| `Pool` | Main orchestrator |
| `Config` | Configuration |
| `ProcessFunc` | User processing function |
| `Stats` | Runtime statistics |

---

**API Version**: 1.0
**Last Updated**: 2025-01-10
