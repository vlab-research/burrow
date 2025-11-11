# Burrow Implementation Plan

## Overview

This document provides a detailed, step-by-step implementation plan for building the Burrow library. Follow this plan to implement a production-ready concurrent Kafka consumer with ordered commits and at-least-once guarantees.

## Prerequisites

Before starting implementation:
- Go 1.21 or higher
- Understanding of Kafka consumer API
- Familiarity with Go concurrency (goroutines, channels, mutexes)
- Access to Kafka cluster for testing

## Phase 1: Project Setup (Day 1)

### Step 1.1: Initialize Go Module

```bash
cd burrow
go mod init github.com/vlab-research/fly/burrow
```

### Step 1.2: Add Dependencies

```bash
go get github.com/confluentinc/confluent-kafka-go/v2/kafka
go get go.uber.org/zap
go get github.com/prometheus/client_golang/prometheus
go get github.com/stretchr/testify
```

**go.mod should contain:**
```go
module github.com/vlab-research/fly/burrow

go 1.21

require (
    github.com/confluentinc/confluent-kafka-go/v2 v2.3.0
    go.uber.org/zap v1.26.0
    github.com/prometheus/client_golang v1.18.0
    github.com/stretchr/testify v1.8.4
)
```

### Step 1.3: Create File Structure

```bash
touch pool.go consumer.go worker.go tracker.go commit.go errors.go types.go config.go metrics.go
mkdir -p tests examples/simple docs
```

## Phase 2: Core Types and Interfaces (Day 1-2)

### Step 2.1: Define Core Types (types.go)

```go
// types.go
package burrow

import (
    "context"
    "github.com/confluentinc/confluent-kafka-go/v2/kafka"
)

// ProcessFunc is the user-defined function for processing a message
type ProcessFunc func(context.Context, *kafka.Message) error

// Job represents a message to be processed by a worker
type Job struct {
    Partition    int32
    Offset       int64
    Message      *kafka.Message
    ProcessFunc  ProcessFunc
    Attempt      int           // Retry attempt number
}

// Result represents the outcome of processing a job
type Result struct {
    Partition    int32
    Offset       int64
    Success      bool
    Error        error
    Attempt      int
    Job          *Job         // Original job for retry
}

// Stats contains runtime statistics
type Stats struct {
    MessagesProcessed  int64
    MessagesFailed     int64
    OffsetsCommitted   int64
    WorkersActive      int
    JobsQueued         int
}
```

**Implementation notes:**
- Keep types simple and focused
- Use standard Kafka types where possible
- Include retry metadata in Job/Result

**Testing:**
```bash
go test -run TestTypes ./...
```

### Step 2.2: Define Configuration (config.go)

```go
// config.go
package burrow

import (
    "time"
    "go.uber.org/zap"
)

// Config contains configuration for the Pool
type Config struct {
    // Worker pool configuration
    NumWorkers     int           // Number of concurrent workers (default: 10)
    JobQueueSize   int           // Size of job queue buffer (default: 1000)
    ResultQueueSize int          // Size of result queue buffer (default: 1000)

    // Commit configuration
    CommitInterval time.Duration // How often to commit offsets (default: 5s)
    CommitBatchSize int          // Max messages before forcing commit (default: 1000)

    // Error handling
    MaxConsecutiveErrors int     // Max consecutive errors before halt (default: 10)
    MaxRetries           int     // Max retries per message (default: 3)
    RetryBackoffBase     time.Duration  // Base backoff duration (default: 100ms)

    // Logging and metrics
    Logger         *zap.Logger   // Logger instance (required)
    EnableMetrics  bool          // Enable Prometheus metrics (default: false)

    // Advanced options
    EnableOrderedProcessing bool // Ensure per-partition ordering (default: false)
    ShutdownTimeout        time.Duration  // Graceful shutdown timeout (default: 30s)
}

// DefaultConfig returns a config with sensible defaults
func DefaultConfig(logger *zap.Logger) Config {
    return Config{
        NumWorkers:           10,
        JobQueueSize:         1000,
        ResultQueueSize:      1000,
        CommitInterval:       5 * time.Second,
        CommitBatchSize:      1000,
        MaxConsecutiveErrors: 10,
        MaxRetries:           3,
        RetryBackoffBase:     100 * time.Millisecond,
        Logger:               logger,
        EnableMetrics:        false,
        EnableOrderedProcessing: false,
        ShutdownTimeout:      30 * time.Second,
    }
}

// Validate checks if config is valid
func (c Config) Validate() error {
    if c.NumWorkers <= 0 {
        return fmt.Errorf("NumWorkers must be > 0, got %d", c.NumWorkers)
    }
    if c.Logger == nil {
        return fmt.Errorf("Logger is required")
    }
    if c.CommitInterval <= 0 {
        return fmt.Errorf("CommitInterval must be > 0")
    }
    return nil
}
```

**Implementation notes:**
- Provide sensible defaults
- Make logger required (critical for debugging)
- Add validation method
- Keep advanced options opt-in

**Testing:**
```bash
go test -run TestConfig ./...
```

## Phase 3: Offset Tracker (Day 2-3)

### Step 3.1: Implement OffsetTracker (tracker.go)

This is the **most critical component** - it enables ordered commits!

```go
// tracker.go
package burrow

import (
    "sync"
    "go.uber.org/zap"
)

// OffsetTracker tracks offset processing status for a single partition
type OffsetTracker struct {
    mu              sync.Mutex
    partition       int32
    processedMap    map[int64]bool    // offset -> completed?
    inflightMap     map[int64]bool    // offset -> currently processing?
    lastCommitted   int64              // Last offset committed to Kafka
    highWatermark   int64              // Highest offset we've seen
    logger          *zap.Logger
}

// NewOffsetTracker creates a new tracker for a partition
func NewOffsetTracker(partition int32, logger *zap.Logger) *OffsetTracker {
    return &OffsetTracker{
        partition:     partition,
        processedMap:  make(map[int64]bool),
        inflightMap:   make(map[int64]bool),
        lastCommitted: -1,  // No offsets committed yet
        highWatermark: -1,
        logger:        logger.With(zap.Int32("partition", partition)),
    }
}

// RecordInflight marks an offset as currently being processed
func (ot *OffsetTracker) RecordInflight(offset int64) {
    ot.mu.Lock()
    defer ot.mu.Unlock()

    ot.inflightMap[offset] = true
    if offset > ot.highWatermark {
        ot.highWatermark = offset
    }

    ot.logger.Debug("recorded inflight",
        zap.Int64("offset", offset),
        zap.Int64("highWatermark", ot.highWatermark))
}

// MarkProcessed records that an offset has been successfully processed
func (ot *OffsetTracker) MarkProcessed(offset int64) {
    ot.mu.Lock()
    defer ot.mu.Unlock()

    ot.processedMap[offset] = true
    delete(ot.inflightMap, offset)

    ot.logger.Debug("marked processed",
        zap.Int64("offset", offset),
        zap.Int("inflight", len(ot.inflightMap)),
        zap.Int("processed", len(ot.processedMap)))
}

// MarkFailed records that an offset processing failed
func (ot *OffsetTracker) MarkFailed(offset int64) {
    ot.mu.Lock()
    defer ot.mu.Unlock()

    // Don't mark as processed - leave gap!
    delete(ot.inflightMap, offset)

    ot.logger.Warn("marked failed",
        zap.Int64("offset", offset),
        zap.Int("inflight", len(ot.inflightMap)))
}

// GetCommittableOffset returns the highest offset that can be safely committed
// This is the CORE ALGORITHM for gap detection!
func (ot *OffsetTracker) GetCommittableOffset() int64 {
    ot.mu.Lock()
    defer ot.mu.Unlock()

    // Find the highest contiguous offset (no gaps)
    committable := ot.lastCommitted
    for offset := ot.lastCommitted + 1; offset <= ot.highWatermark; offset++ {
        if !ot.processedMap[offset] {
            // Gap found! Can't commit beyond this point
            break
        }
        committable = offset
    }

    return committable
}

// GetLastCommitted returns the last committed offset
func (ot *OffsetTracker) GetLastCommitted() int64 {
    ot.mu.Lock()
    defer ot.mu.Unlock()
    return ot.lastCommitted
}

// CommitOffset updates the last committed offset and cleans up old entries
func (ot *OffsetTracker) CommitOffset(offset int64) {
    ot.mu.Lock()
    defer ot.mu.Unlock()

    ot.lastCommitted = offset

    // Clean up processed map to prevent memory leak
    for o := range ot.processedMap {
        if o <= offset {
            delete(ot.processedMap, o)
        }
    }

    ot.logger.Info("committed offset",
        zap.Int64("offset", offset),
        zap.Int("remaining_tracked", len(ot.processedMap)))
}

// GetInflightCount returns number of messages currently being processed
func (ot *OffsetTracker) GetInflightCount() int {
    ot.mu.Lock()
    defer ot.mu.Unlock()
    return len(ot.inflightMap)
}

// WaitForInflight blocks until all inflight messages complete (for rebalance)
func (ot *OffsetTracker) WaitForInflight() {
    ticker := time.NewTicker(100 * time.Millisecond)
    defer ticker.Stop()

    for {
        <-ticker.C
        if ot.GetInflightCount() == 0 {
            return
        }
        ot.logger.Info("waiting for inflight messages to complete",
            zap.Int("inflight", ot.GetInflightCount()))
    }
}
```

**Implementation notes:**
- Thread-safe with mutex (will be called from multiple goroutines)
- Cleanup old entries after commit (prevents memory leak)
- Log at appropriate levels (Debug for normal, Warn for failures)
- `GetCommittableOffset()` is the **core algorithm** - test thoroughly!

**Testing (CRITICAL):**
```go
// tracker_test.go
func TestOffsetTracker_GetCommittableOffset(t *testing.T) {
    tests := []struct {
        name       string
        processed  []int64
        want       int64
    }{
        {
            name:      "all processed in order",
            processed: []int64{0, 1, 2, 3, 4},
            want:      4,
        },
        {
            name:      "gap at offset 2",
            processed: []int64{0, 1, 3, 4, 5},
            want:      1,  // Can only commit up to 1 (gap at 2)
        },
        {
            name:      "gap at beginning",
            processed: []int64{1, 2, 3, 4},
            want:      -1,  // Can't commit anything (missing 0)
        },
        {
            name:      "single message",
            processed: []int64{0},
            want:      0,
        },
        {
            name:      "empty",
            processed: []int64{},
            want:      -1,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            logger, _ := zap.NewDevelopment()
            tracker := NewOffsetTracker(0, logger)

            // Mark offsets as processed
            for _, offset := range tt.processed {
                tracker.RecordInflight(offset)
                tracker.MarkProcessed(offset)
            }

            got := tracker.GetCommittableOffset()
            if got != tt.want {
                t.Errorf("GetCommittableOffset() = %d, want %d", got, tt.want)
            }
        })
    }
}
```

Run tests:
```bash
go test -v -run TestOffsetTracker ./...
```

## Phase 4: Worker Pool (Day 3-4)

### Step 4.1: Implement Worker (worker.go)

```go
// worker.go
package burrow

import (
    "context"
    "time"
    "go.uber.org/zap"
)

// Worker processes jobs from the jobs channel
type Worker struct {
    id           int
    jobsChan     <-chan *Job
    resultsChan  chan<- *Result
    logger       *zap.Logger
}

// run starts the worker loop (runs in its own goroutine)
func (w *Worker) run(ctx context.Context) {
    w.logger.Info("worker started", zap.Int("worker_id", w.id))
    defer w.logger.Info("worker stopped", zap.Int("worker_id", w.id))

    for {
        select {
        case <-ctx.Done():
            return

        case job := <-w.jobsChan:
            w.processJob(ctx, job)
        }
    }
}

// processJob executes the user's ProcessFunc and sends result
func (w *Worker) processJob(ctx context.Context, job *Job) {
    start := time.Now()

    // Call user's processing function
    err := job.ProcessFunc(ctx, job.Message)

    duration := time.Since(start)

    // Create result
    result := &Result{
        Partition: job.Partition,
        Offset:    job.Offset,
        Success:   err == nil,
        Error:     err,
        Attempt:   job.Attempt,
        Job:       job,
    }

    // Log result
    if err != nil {
        w.logger.Error("job failed",
            zap.Int("worker_id", w.id),
            zap.Int32("partition", job.Partition),
            zap.Int64("offset", job.Offset),
            zap.Int("attempt", job.Attempt),
            zap.Duration("duration", duration),
            zap.Error(err))
    } else {
        w.logger.Debug("job succeeded",
            zap.Int("worker_id", w.id),
            zap.Int32("partition", job.Partition),
            zap.Int64("offset", job.Offset),
            zap.Duration("duration", duration))
    }

    // Send result (blocks if result channel is full - backpressure)
    select {
    case w.resultsChan <- result:
    case <-ctx.Done():
        return
    }
}

// WorkerPool manages a pool of workers
type WorkerPool struct {
    numWorkers   int
    jobsChan     chan *Job
    resultsChan  chan *Result
    logger       *zap.Logger
    ctx          context.Context
    cancel       context.CancelFunc
}

// NewWorkerPool creates a new worker pool
func NewWorkerPool(numWorkers, jobQueueSize, resultQueueSize int, logger *zap.Logger) *WorkerPool {
    ctx, cancel := context.WithCancel(context.Background())

    return &WorkerPool{
        numWorkers:   numWorkers,
        jobsChan:     make(chan *Job, jobQueueSize),
        resultsChan:  make(chan *Result, resultQueueSize),
        logger:       logger,
        ctx:          ctx,
        cancel:       cancel,
    }
}

// Start launches all workers
func (wp *WorkerPool) Start() {
    wp.logger.Info("starting worker pool", zap.Int("num_workers", wp.numWorkers))

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

// Stop gracefully stops all workers
func (wp *WorkerPool) Stop() {
    wp.logger.Info("stopping worker pool")
    wp.cancel()
    close(wp.jobsChan)
}

// SubmitJob adds a job to the queue (blocks if queue is full)
func (wp *WorkerPool) SubmitJob(ctx context.Context, job *Job) error {
    select {
    case wp.jobsChan <- job:
        return nil
    case <-ctx.Done():
        return ctx.Err()
    }
}

// Results returns the results channel
func (wp *WorkerPool) Results() <-chan *Result {
    return wp.resultsChan
}
```

**Implementation notes:**
- Each worker is a goroutine with a simple loop
- Workers pull from shared jobs channel (natural load balancing)
- Blocking send to results channel provides backpressure
- Log at debug level for success, error level for failures
- Context cancellation for clean shutdown

**Testing:**
```bash
go test -run TestWorkerPool ./...
```

## Phase 5: Error Tracking (Day 4)

### Step 5.1: Implement ErrorTracker (errors.go)

```go
// errors.go
package burrow

import (
    "sync"
    "go.uber.org/zap"
)

// ErrorTracker tracks errors and halts processing if threshold exceeded
// Inspired by Eddies' error tracking pattern
type ErrorTracker struct {
    mu                sync.Mutex
    consecutiveErrors int
    maxConsecutive    int
    totalErrors       int64
    logger            *zap.Logger
}

// NewErrorTracker creates a new error tracker
func NewErrorTracker(maxConsecutive int, logger *zap.Logger) *ErrorTracker {
    return &ErrorTracker{
        maxConsecutive: maxConsecutive,
        logger:         logger,
    }
}

// RecordError records an error and returns true if should halt
func (et *ErrorTracker) RecordError(partition int32, offset int64, err error) bool {
    et.mu.Lock()
    defer et.mu.Unlock()

    et.consecutiveErrors++
    et.totalErrors++

    shouldHalt := et.consecutiveErrors >= et.maxConsecutive

    if shouldHalt {
        et.logger.Error("error threshold exceeded, halting",
            zap.Int("consecutive_errors", et.consecutiveErrors),
            zap.Int("max_consecutive", et.maxConsecutive),
            zap.Int64("total_errors", et.totalErrors),
            zap.Int32("partition", partition),
            zap.Int64("offset", offset),
            zap.Error(err))
    } else {
        et.logger.Warn("processing error",
            zap.Int("consecutive_errors", et.consecutiveErrors),
            zap.Int32("partition", partition),
            zap.Int64("offset", offset),
            zap.Error(err))
    }

    return shouldHalt
}

// RecordSuccess resets consecutive error counter
func (et *ErrorTracker) RecordSuccess() {
    et.mu.Lock()
    defer et.mu.Unlock()

    if et.consecutiveErrors > 0 {
        et.logger.Debug("resetting consecutive error counter",
            zap.Int("was", et.consecutiveErrors))
        et.consecutiveErrors = 0
    }
}

// GetStats returns error statistics
func (et *ErrorTracker) GetStats() (consecutive int, total int64) {
    et.mu.Lock()
    defer et.mu.Unlock()
    return et.consecutiveErrors, et.totalErrors
}

// isRetriable determines if an error should be retried
func isRetriable(err error) bool {
    if err == nil {
        return false
    }

    // TODO: Add specific error type checking
    // For now, retry most errors
    return true
}

// calculateBackoff computes exponential backoff duration
func calculateBackoff(attempt int, base time.Duration) time.Duration {
    // Exponential: base * 2^attempt, capped at 60 seconds
    backoff := base * time.Duration(1<<uint(attempt))
    if backoff > 60*time.Second {
        backoff = 60 * time.Second
    }
    return backoff
}
```

**Implementation notes:**
- Thread-safe with mutex
- Tracks both consecutive and total errors
- Reset consecutive on success
- Provide backoff calculation helper

**Testing:**
```bash
go test -run TestErrorTracker ./...
```

## Phase 6: Commit Manager (Day 5)

### Step 6.1: Implement CommitManager (commit.go)

```go
// commit.go
package burrow

import (
    "context"
    "sync"
    "time"
    "github.com/confluentinc/confluent-kafka-go/v2/kafka"
    "go.uber.org/zap"
)

// CommitManager handles periodic offset commits
type CommitManager struct {
    consumer        *kafka.Consumer
    trackers        map[int32]*OffsetTracker
    trackersMu      sync.RWMutex
    commitInterval  time.Duration
    commitBatchSize int
    messagesSinceCommit int64
    logger          *zap.Logger
}

// NewCommitManager creates a new commit manager
func NewCommitManager(consumer *kafka.Consumer, commitInterval time.Duration, commitBatchSize int, logger *zap.Logger) *CommitManager {
    return &CommitManager{
        consumer:        consumer,
        trackers:        make(map[int32]*OffsetTracker),
        commitInterval:  commitInterval,
        commitBatchSize: commitBatchSize,
        logger:          logger,
    }
}

// RegisterTracker adds a tracker for a partition
func (cm *CommitManager) RegisterTracker(partition int32, tracker *OffsetTracker) {
    cm.trackersMu.Lock()
    defer cm.trackersMu.Unlock()
    cm.trackers[partition] = tracker
}

// UnregisterTracker removes a tracker (for rebalance)
func (cm *CommitManager) UnregisterTracker(partition int32) {
    cm.trackersMu.Lock()
    defer cm.trackersMu.Unlock()
    delete(cm.trackers, partition)
}

// RecordMessage increments message counter (for batch commit trigger)
func (cm *CommitManager) RecordMessage() {
    cm.messagesSinceCommit++
}

// Start begins the commit loop
func (cm *CommitManager) Start(ctx context.Context) {
    ticker := time.NewTicker(cm.commitInterval)
    defer ticker.Stop()

    cm.logger.Info("commit manager started",
        zap.Duration("interval", cm.commitInterval),
        zap.Int("batch_size", cm.commitBatchSize))

    for {
        select {
        case <-ctx.Done():
            // Final commit before shutdown
            cm.tryCommit(ctx)
            cm.logger.Info("commit manager stopped")
            return

        case <-ticker.C:
            cm.tryCommit(ctx)

        default:
            // Check if we've hit batch size threshold
            if cm.messagesSinceCommit >= int64(cm.commitBatchSize) {
                cm.tryCommit(ctx)
            }
            time.Sleep(100 * time.Millisecond)
        }
    }
}

// tryCommit attempts to commit offsets for all partitions
func (cm *CommitManager) tryCommit(ctx context.Context) error {
    cm.trackersMu.RLock()
    defer cm.trackersMu.RUnlock()

    if len(cm.trackers) == 0 {
        return nil  // No partitions assigned
    }

    // Collect committable offsets
    offsets := make([]kafka.TopicPartition, 0)

    for partition, tracker := range cm.trackers {
        committable := tracker.GetCommittableOffset()
        lastCommitted := tracker.GetLastCommitted()

        if committable > lastCommitted {
            // Kafka commits "next offset to read", so add 1
            offsets = append(offsets, kafka.TopicPartition{
                Partition: partition,
                Offset:    kafka.Offset(committable + 1),
            })
        }
    }

    if len(offsets) == 0 {
        cm.logger.Debug("no offsets to commit")
        return nil
    }

    // Synchronous commit for safety
    cm.logger.Info("committing offsets",
        zap.Int("partitions", len(offsets)),
        zap.Any("offsets", offsets))

    _, err := cm.consumer.CommitOffsets(offsets)
    if err != nil {
        cm.logger.Error("failed to commit offsets",
            zap.Error(err),
            zap.Any("offsets", offsets))
        return err
    }

    // Update trackers
    for _, tp := range offsets {
        tracker := cm.trackers[tp.Partition]
        tracker.CommitOffset(int64(tp.Offset) - 1)  // Convert back to message offset
    }

    // Reset counter
    cm.messagesSinceCommit = 0

    cm.logger.Info("successfully committed offsets",
        zap.Int("partitions", len(offsets)))

    return nil
}
```

**Implementation notes:**
- Thread-safe tracker management
- Periodic + batch-size-based commits
- Synchronous commits for safety
- Final commit on shutdown
- Clean logging of commit activity

**Testing:**
```bash
go test -run TestCommitManager ./...
```

**Continue to Part 2 for Phases 7-10...**
