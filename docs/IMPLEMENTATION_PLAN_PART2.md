# Burrow Implementation Plan - Part 2

## Phase 7: Main Pool Orchestrator (Day 6-7)

### Step 7.1: Implement Pool (pool.go)

This is the main entry point - ties everything together!

```go
// pool.go
package burrow

import (
    "context"
    "fmt"
    "sync"
    "time"
    "github.com/confluentinc/confluent-kafka-go/v2/kafka"
    "go.uber.org/zap"
)

// Pool is the main entry point for Burrow
type Pool struct {
    consumer        *kafka.Consumer
    config          Config
    workerPool      *WorkerPool
    commitManager   *CommitManager
    errorTracker    *ErrorTracker
    offsetTrackers  map[int32]*OffsetTracker
    trackersMu      sync.RWMutex
    logger          *zap.Logger
    ctx             context.Context
    cancel          context.CancelFunc
    wg              sync.WaitGroup
}

// NewPool creates a new Burrow pool
func NewPool(consumer *kafka.Consumer, config Config) (*Pool, error) {
    // Validate config
    if err := config.Validate(); err != nil {
        return nil, fmt.Errorf("invalid config: %w", err)
    }

    ctx, cancel := context.WithCancel(context.Background())

    // Create worker pool
    workerPool := NewWorkerPool(
        config.NumWorkers,
        config.JobQueueSize,
        config.ResultQueueSize,
        config.Logger,
    )

    // Create commit manager
    commitManager := NewCommitManager(
        consumer,
        config.CommitInterval,
        config.CommitBatchSize,
        config.Logger,
    )

    // Create error tracker
    errorTracker := NewErrorTracker(
        config.MaxConsecutiveErrors,
        config.Logger,
    )

    pool := &Pool{
        consumer:       consumer,
        config:         config,
        workerPool:     workerPool,
        commitManager:  commitManager,
        errorTracker:   errorTracker,
        offsetTrackers: make(map[int32]*OffsetTracker),
        logger:         config.Logger,
        ctx:            ctx,
        cancel:         cancel,
    }

    return pool, nil
}

// Run starts the pool and processes messages until context is cancelled
func (p *Pool) Run(ctx context.Context, processFunc ProcessFunc) error {
    p.logger.Info("starting pool",
        zap.Int("num_workers", p.config.NumWorkers),
        zap.Duration("commit_interval", p.config.CommitInterval))

    // Start worker pool
    p.workerPool.Start()

    // Start commit manager
    p.wg.Add(1)
    go func() {
        defer p.wg.Done()
        p.commitManager.Start(p.ctx)
    }()

    // Start result processor
    p.wg.Add(1)
    go func() {
        defer p.wg.Done()
        p.processResults()
    }()

    // Main poll loop (single-threaded - Kafka consumer is NOT thread-safe)
    err := p.pollLoop(ctx, processFunc)

    // Cleanup
    p.logger.Info("shutting down pool")
    p.cancel()
    p.wg.Wait()
    p.workerPool.Stop()

    return err
}

// pollLoop reads messages from Kafka and dispatches to workers
func (p *Pool) pollLoop(ctx context.Context, processFunc ProcessFunc) error {
    for {
        select {
        case <-ctx.Done():
            return ctx.Err()

        default:
            // Poll Kafka (100ms timeout)
            msg, err := p.consumer.ReadMessage(100 * time.Millisecond)
            if err != nil {
                // Timeout or transient error - continue
                kafkaErr, ok := err.(kafka.Error)
                if ok && kafkaErr.Code() == kafka.ErrTimedOut {
                    continue
                }
                p.logger.Warn("kafka read error", zap.Error(err))
                continue
            }

            // Create job
            job := &Job{
                Partition:   msg.TopicPartition.Partition,
                Offset:      int64(msg.TopicPartition.Offset),
                Message:     msg,
                ProcessFunc: processFunc,
                Attempt:     0,
            }

            // Get or create tracker for this partition
            tracker := p.getOrCreateTracker(job.Partition)

            // Record inflight
            tracker.RecordInflight(job.Offset)

            // Submit to worker pool (blocks if queue is full - backpressure!)
            if err := p.workerPool.SubmitJob(ctx, job); err != nil {
                return err
            }

            // Record message for batch commit trigger
            p.commitManager.RecordMessage()
        }
    }
}

// processResults handles results from workers
func (p *Pool) processResults() {
    for result := range p.workerPool.Results() {
        tracker := p.getTracker(result.Partition)
        if tracker == nil {
            p.logger.Warn("no tracker for partition", zap.Int32("partition", result.Partition))
            continue
        }

        if result.Success {
            // Success!
            tracker.MarkProcessed(result.Offset)
            p.errorTracker.RecordSuccess()

            p.logger.Debug("message processed successfully",
                zap.Int32("partition", result.Partition),
                zap.Int64("offset", result.Offset))

        } else {
            // Failure - handle retry or permanent failure
            p.handleFailure(result, tracker)
        }
    }
}

// handleFailure handles a failed message (retry or permanent failure)
func (p *Pool) handleFailure(result *Result, tracker *OffsetTracker) {
    // Check if retriable
    if result.Attempt < p.config.MaxRetries && isRetriable(result.Error) {
        // Retry with backoff
        backoff := calculateBackoff(result.Attempt, p.config.RetryBackoffBase)

        p.logger.Info("retrying message",
            zap.Int32("partition", result.Partition),
            zap.Int64("offset", result.Offset),
            zap.Int("attempt", result.Attempt+1),
            zap.Int("max_retries", p.config.MaxRetries),
            zap.Duration("backoff", backoff),
            zap.Error(result.Error))

        // Schedule retry
        time.AfterFunc(backoff, func() {
            retryJob := &Job{
                Partition:   result.Partition,
                Offset:      result.Offset,
                Message:     result.Job.Message,
                ProcessFunc: result.Job.ProcessFunc,
                Attempt:     result.Attempt + 1,
            }

            // Re-submit (best effort - if context cancelled, will fail gracefully)
            p.workerPool.SubmitJob(p.ctx, retryJob)
        })

    } else {
        // Permanent failure - mark as failed (leaves gap)
        tracker.MarkFailed(result.Offset)

        p.logger.Error("permanent message failure",
            zap.Int32("partition", result.Partition),
            zap.Int64("offset", result.Offset),
            zap.Int("attempts", result.Attempt+1),
            zap.Error(result.Error))

        // Check error threshold
        shouldHalt := p.errorTracker.RecordError(result.Partition, result.Offset, result.Error)
        if shouldHalt {
            p.logger.Error("error threshold exceeded, halting pool")
            p.cancel()  // Halt processing
        }
    }
}

// getOrCreateTracker gets or creates a tracker for a partition
func (p *Pool) getOrCreateTracker(partition int32) *OffsetTracker {
    p.trackersMu.RLock()
    tracker, exists := p.offsetTrackers[partition]
    p.trackersMu.RUnlock()

    if exists {
        return tracker
    }

    // Create new tracker
    p.trackersMu.Lock()
    defer p.trackersMu.Unlock()

    // Check again (double-checked locking)
    tracker, exists = p.offsetTrackers[partition]
    if exists {
        return tracker
    }

    tracker = NewOffsetTracker(partition, p.logger)
    p.offsetTrackers[partition] = tracker
    p.commitManager.RegisterTracker(partition, tracker)

    p.logger.Info("created tracker for partition", zap.Int32("partition", partition))

    return tracker
}

// getTracker gets a tracker for a partition (may return nil)
func (p *Pool) getTracker(partition int32) *OffsetTracker {
    p.trackersMu.RLock()
    defer p.trackersMu.RUnlock()
    return p.offsetTrackers[partition]
}

// GetStats returns runtime statistics
func (p *Pool) GetStats() Stats {
    // TODO: Implement statistics collection
    return Stats{}
}
```

**Implementation notes:**
- Pool orchestrates all components
- Single-threaded Kafka poll (thread safety)
- Separate goroutines for result processing and commits
- Backpressure via bounded channels
- Clean shutdown with WaitGroup

**Testing:**
```bash
go test -run TestPool ./...
```

## Phase 8: Partition Rebalance Handling (Day 7)

### Step 8.1: Add Rebalance Callbacks (pool.go)

```go
// Add to Pool struct
type Pool struct {
    // ... existing fields ...
    rebalanceCb kafka.RebalanceCb
}

// setupRebalanceCallback configures partition rebalance handling
func (p *Pool) setupRebalanceCallback() {
    p.rebalanceCb = func(c *kafka.Consumer, event kafka.Event) error {
        switch ev := event.(type) {
        case kafka.AssignedPartitions:
            p.onPartitionsAssigned(ev.Partitions)
            return c.Assign(ev.Partitions)

        case kafka.RevokedPartitions:
            p.onPartitionsRevoked(ev.Partitions)
            return c.Unassign()
        }
        return nil
    }
}

// onPartitionsAssigned handles new partition assignment
func (p *Pool) onPartitionsAssigned(partitions []kafka.TopicPartition) {
    p.logger.Info("partitions assigned",
        zap.Int("count", len(partitions)),
        zap.Any("partitions", partitions))

    // Trackers will be created lazily when first message arrives
}

// onPartitionsRevoked handles partition revocation
func (p *Pool) onPartitionsRevoked(partitions []kafka.TopicPartition) {
    p.logger.Info("partitions revoked",
        zap.Int("count", len(partitions)),
        zap.Any("partitions", partitions))

    // Wait for inflight messages to complete
    for _, tp := range partitions {
        tracker := p.getTracker(tp.Partition)
        if tracker != nil {
            p.logger.Info("waiting for inflight messages",
                zap.Int32("partition", tp.Partition),
                zap.Int("inflight", tracker.GetInflightCount()))

            tracker.WaitForInflight()
        }
    }

    // Final commit before losing partition
    p.commitManager.tryCommit(p.ctx)

    // Cleanup trackers
    p.trackersMu.Lock()
    for _, tp := range partitions {
        delete(p.offsetTrackers, tp.Partition)
        p.commitManager.UnregisterTracker(tp.Partition)
    }
    p.trackersMu.Unlock()

    p.logger.Info("partitions cleanup complete",
        zap.Int("count", len(partitions)))
}

// Update NewPool to setup callback
func NewPool(consumer *kafka.Consumer, config Config) (*Pool, error) {
    // ... existing code ...

    pool.setupRebalanceCallback()

    // Register callback with consumer
    // Note: This should be done before SubscribeTopics
    // Update consumer creation in user code to pass rebalanceCb

    return pool, nil
}
```

**Implementation notes:**
- Wait for inflight messages before releasing partition
- Final commit to Kafka before cleanup
- Remove trackers for revoked partitions
- Log partition changes for debugging

## Phase 9: Metrics (Day 8)

### Step 9.1: Add Prometheus Metrics (metrics.go)

```go
// metrics.go
package burrow

import (
    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promauto"
)

var (
    messagesProcessed = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "burrow_messages_processed_total",
            Help: "Total number of messages processed",
        },
        []string{"partition", "status"}, // status: success, failed, retried
    )

    processingDuration = promauto.NewHistogramVec(
        prometheus.HistogramOpts{
            Name:    "burrow_processing_duration_seconds",
            Help:    "Time spent processing messages",
            Buckets: prometheus.DefBuckets,
        },
        []string{"partition"},
    )

    offsetsCommitted = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "burrow_offsets_committed_total",
            Help: "Total number of offsets committed",
        },
        []string{"partition"},
    )

    offsetLag = promauto.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "burrow_offset_lag",
            Help: "Offset lag (high watermark - last committed)",
        },
        []string{"partition"},
    )

    inflightMessages = promauto.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "burrow_inflight_messages",
            Help: "Number of messages currently being processed",
        },
        []string{"partition"},
    )

    workerUtilization = promauto.NewGauge(
        prometheus.GaugeOpts{
            Name: "burrow_worker_utilization",
            Help: "Percentage of workers currently busy (0-1)",
        },
    )

    errorCount = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "burrow_errors_total",
            Help: "Total number of processing errors",
        },
        []string{"partition", "retriable"},
    )
)

// recordMetrics should be called from appropriate places in the code
func (p *Pool) recordProcessedMessage(partition int32, success bool, duration time.Duration) {
    if !p.config.EnableMetrics {
        return
    }

    status := "success"
    if !success {
        status = "failed"
    }

    messagesProcessed.WithLabelValues(
        fmt.Sprintf("%d", partition),
        status,
    ).Inc()

    processingDuration.WithLabelValues(
        fmt.Sprintf("%d", partition),
    ).Observe(duration.Seconds())
}

func (p *Pool) recordCommit(partition int32, offset int64) {
    if !p.config.EnableMetrics {
        return
    }

    offsetsCommitted.WithLabelValues(
        fmt.Sprintf("%d", partition),
    ).Inc()
}

func (p *Pool) updateInflightGauge(partition int32, count int) {
    if !p.config.EnableMetrics {
        return
    }

    inflightMessages.WithLabelValues(
        fmt.Sprintf("%d", partition),
    ).Set(float64(count))
}
```

**Implementation notes:**
- Use Prometheus for metrics
- Track key indicators: throughput, latency, errors, lag
- Make metrics optional (config flag)
- Expose metrics via HTTP endpoint

## Phase 10: Testing (Day 9-10)

### Step 10.1: Unit Tests

Create comprehensive unit tests for each component:

```go
// tests/tracker_test.go - Already covered in Phase 3

// tests/worker_test.go
func TestWorker_ProcessJob(t *testing.T) {
    logger, _ := zap.NewDevelopment()
    jobsChan := make(chan *Job, 1)
    resultsChan := make(chan *Result, 1)

    worker := &Worker{
        id:          0,
        jobsChan:    jobsChan,
        resultsChan: resultsChan,
        logger:      logger,
    }

    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    go worker.run(ctx)

    // Test successful processing
    job := &Job{
        Partition: 0,
        Offset:    0,
        Message:   &kafka.Message{Value: []byte("test")},
        ProcessFunc: func(ctx context.Context, msg *kafka.Message) error {
            return nil
        },
    }

    jobsChan <- job

    result := <-resultsChan
    assert.True(t, result.Success)
    assert.NoError(t, result.Error)
}

// tests/error_tracker_test.go
func TestErrorTracker_Threshold(t *testing.T) {
    logger, _ := zap.NewDevelopment()
    tracker := NewErrorTracker(3, logger)

    // First 2 errors - should not halt
    assert.False(t, tracker.RecordError(0, 0, errors.New("error 1")))
    assert.False(t, tracker.RecordError(0, 1, errors.New("error 2")))

    // 3rd error - should halt
    assert.True(t, tracker.RecordError(0, 2, errors.New("error 3")))

    // Success resets counter
    tracker.RecordSuccess()
    assert.False(t, tracker.RecordError(0, 3, errors.New("error 4")))
}
```

### Step 10.2: Integration Tests

```go
// tests/integration_test.go
func TestPool_EndToEnd(t *testing.T) {
    // This requires a real Kafka instance - use testcontainers
    // See example below
}
```

Example with testcontainers:

```go
func TestPool_WithKafka(t *testing.T) {
    ctx := context.Background()

    // Start Kafka container
    kafkaContainer, err := kafka.RunContainer(ctx)
    require.NoError(t, err)
    defer kafkaContainer.Terminate(ctx)

    brokers, err := kafkaContainer.Brokers(ctx)
    require.NoError(t, err)

    // Create topic
    topic := "test-topic"
    createTopic(t, brokers, topic)

    // Produce test messages
    produceMessages(t, brokers, topic, 100)

    // Create consumer
    consumer, err := kafka.NewConsumer(&kafka.ConfigMap{
        "bootstrap.servers": brokers[0],
        "group.id":          "test-group",
        "auto.offset.reset": "earliest",
        "enable.auto.commit": false,
    })
    require.NoError(t, err)
    defer consumer.Close()

    consumer.SubscribeTopics([]string{topic}, nil)

    // Create pool
    logger, _ := zap.NewDevelopment()
    pool, err := NewPool(consumer, DefaultConfig(logger))
    require.NoError(t, err)

    // Process messages
    processed := int32(0)
    processFunc := func(ctx context.Context, msg *kafka.Message) error {
        atomic.AddInt32(&processed, 1)
        return nil
    }

    // Run for 10 seconds
    ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
    defer cancel()

    err = pool.Run(ctx, processFunc)
    assert.True(t, errors.Is(err, context.DeadlineExceeded))

    // Verify all messages processed
    assert.Equal(t, int32(100), atomic.LoadInt32(&processed))
}
```

### Step 10.3: Benchmark Tests

```go
// tests/benchmark_test.go
func BenchmarkPool_Throughput(b *testing.B) {
    // TODO: Implement throughput benchmark
}

func BenchmarkOffsetTracker_GetCommittable(b *testing.B) {
    logger, _ := zap.NewDevelopment()
    tracker := NewOffsetTracker(0, logger)

    // Seed with 1000 processed offsets
    for i := 0; i < 1000; i++ {
        tracker.RecordInflight(int64(i))
        tracker.MarkProcessed(int64(i))
    }

    b.ResetTimer()

    for i := 0; i < b.N; i++ {
        tracker.GetCommittableOffset()
    }
}
```

## Phase 11: Documentation & Examples (Day 11-12)

### Step 11.1: Create Examples

```go
// examples/simple/main.go - Basic usage
// examples/with-retry/main.go - Custom retry logic
// examples/with-metrics/main.go - Prometheus integration
// examples/ordered-processing/main.go - Ordered processing mode
```

### Step 11.2: Add Godoc Comments

Ensure all public APIs have comprehensive godoc comments.

### Step 11.3: Create Tutorial

Write a tutorial guide showing common use cases.

## Phase 12: Production Hardening (Day 13-14)

### Step 12.1: Performance Profiling

```bash
go test -cpuprofile cpu.prof -memprofile mem.prof -bench .
go tool pprof cpu.prof
go tool pprof mem.prof
```

### Step 12.2: Race Detector

```bash
go test -race ./...
```

### Step 12.3: Memory Leak Detection

Use pprof to detect memory leaks:

```bash
go test -memprofile mem.prof
go tool pprof -alloc_space mem.prof
```

### Step 12.4: Load Testing

Create load tests with high message volume:

```bash
# 1 million messages, 100 workers
./load-test --messages 1000000 --workers 100
```

## Final Checklist

### Code Quality
- [ ] All unit tests passing
- [ ] Integration tests with real Kafka passing
- [ ] No race conditions (go test -race)
- [ ] No memory leaks (pprof)
- [ ] Code coverage > 80%
- [ ] golangci-lint passing
- [ ] godoc comments on all public APIs

### Functionality
- [ ] Concurrent processing works
- [ ] Offset tracking accurate
- [ ] Gap detection correct
- [ ] Commits ordered properly
- [ ] At-least-once guaranteed
- [ ] Retry logic works
- [ ] Error threshold works
- [ ] Rebalance handling works
- [ ] Graceful shutdown works
- [ ] Metrics accurate

### Documentation
- [ ] README complete
- [ ] Architecture doc complete
- [ ] API documentation complete
- [ ] Examples working
- [ ] Tutorial written
- [ ] Design decisions documented

### Production Ready
- [ ] Performance acceptable (benchmarks)
- [ ] Memory usage acceptable
- [ ] No goroutine leaks
- [ ] Proper logging
- [ ] Metrics exposed
- [ ] Error handling robust
- [ ] Configuration validated

## Deployment

Once complete, deploy to production:

1. Tag release: `git tag v1.0.0`
2. Push to GitHub: `git push origin v1.0.0`
3. Publish to pkg.go.dev (automatic)
4. Announce to team
5. Monitor metrics in production

## Estimated Timeline

- **Phase 1-2**: Days 1-2 (Setup + Types)
- **Phase 3**: Days 2-3 (Offset Tracker - CRITICAL)
- **Phase 4**: Days 3-4 (Worker Pool)
- **Phase 5**: Day 4 (Error Tracking)
- **Phase 6**: Day 5 (Commit Manager)
- **Phase 7**: Days 6-7 (Pool Orchestrator)
- **Phase 8**: Day 7 (Rebalance)
- **Phase 9**: Day 8 (Metrics)
- **Phase 10**: Days 9-10 (Testing)
- **Phase 11**: Days 11-12 (Documentation)
- **Phase 12**: Days 13-14 (Hardening)

**Total: ~2-3 weeks for production-ready library**

---

**Good luck with implementation!** 🚀
