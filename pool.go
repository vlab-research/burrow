// pool.go
package burrow

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/confluentinc/confluent-kafka-go/v2/kafka"
	"go.uber.org/zap"
)

// Pool is the main entry point for Burrow
type Pool struct {
	consumer       *kafka.Consumer
	config         Config
	workerPool     *WorkerPool
	commitManager  *CommitManager
	errorTracker   *ErrorTracker
	offsetTrackers map[int32]*OffsetTracker
	trackersMu     sync.RWMutex
	logger         *zap.Logger
	ctx            context.Context
	cancel         context.CancelFunc
	wg             sync.WaitGroup
	rebalanceCb    kafka.RebalanceCb

	// Statistics (atomic counters)
	statsMessagesProcessed int64
	statsMessagesFailed    int64
	statsOffsetsCommitted  int64
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

	// Create error tracker
	errorTracker := NewErrorTracker(
		config.MaxConsecutiveErrors,
		config.Logger,
	)

	pool := &Pool{
		consumer:       consumer,
		config:         config,
		workerPool:     workerPool,
		errorTracker:   errorTracker,
		offsetTrackers: make(map[int32]*OffsetTracker),
		logger:         config.Logger,
		ctx:            ctx,
		cancel:         cancel,
	}

	// Create commit manager with stats counter
	commitManager := NewCommitManager(
		consumer,
		config.CommitInterval,
		config.CommitBatchSize,
		config.Logger,
		&pool.statsOffsetsCommitted,
	)
	pool.commitManager = commitManager

	// Setup rebalance callback
	pool.setupRebalanceCallback()

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
			atomic.AddInt64(&p.statsMessagesProcessed, 1)

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

		// Schedule retry with proper context handling
		retryJob := &Job{
			Partition:   result.Partition,
			Offset:      result.Offset,
			Message:     result.Job.Message,
			ProcessFunc: result.Job.ProcessFunc,
			Attempt:     result.Attempt + 1,
		}

		// Launch goroutine with proper cleanup
		go func(job *Job) {
			select {
			case <-time.After(backoff):
				// Try to submit retry job
				if err := p.workerPool.SubmitJob(p.ctx, job); err != nil {
					// Context cancelled or other error - mark as failed
					p.logger.Warn("failed to submit retry job",
						zap.Int32("partition", job.Partition),
						zap.Int64("offset", job.Offset),
						zap.Error(err))
					tracker.MarkFailed(job.Offset)
				}
			case <-p.ctx.Done():
				// Context cancelled - mark as failed
				p.logger.Debug("retry cancelled due to context",
					zap.Int32("partition", job.Partition),
					zap.Int64("offset", job.Offset))
				tracker.MarkFailed(job.Offset)
				return
			}
		}(retryJob)

	} else {
		// Permanent failure - mark as failed (leaves gap)
		tracker.MarkFailed(result.Offset)
		atomic.AddInt64(&p.statsMessagesFailed, 1)

		p.logger.Error("permanent message failure",
			zap.Int32("partition", result.Partition),
			zap.Int64("offset", result.Offset),
			zap.Int("attempts", result.Attempt+1),
			zap.Error(result.Error))

		// Check error threshold
		shouldHalt := p.errorTracker.RecordError(result.Partition, result.Offset, result.Error)
		if shouldHalt {
			p.logger.Error("error threshold exceeded, halting pool")
			p.cancel() // Halt processing
		}
	}
}

// getOrCreateTracker gets or creates a tracker for a partition
func (p *Pool) getOrCreateTracker(partition int32) *OffsetTracker {
	p.trackersMu.Lock()
	defer p.trackersMu.Unlock()

	if tracker, exists := p.offsetTrackers[partition]; exists {
		return tracker
	}

	tracker := NewOffsetTracker(partition, p.logger)
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

			// Wait up to 30 seconds for inflight to complete
			if err := tracker.WaitForInflight(p.ctx, 30*time.Second); err != nil {
				p.logger.Error("failed to wait for inflight messages",
					zap.Int32("partition", tp.Partition),
					zap.Error(err))
				// Continue anyway - we tried our best
			}
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

// GetStats returns runtime statistics
func (p *Pool) GetStats() Stats {
	p.trackersMu.RLock()
	jobsQueued := 0
	workersActive := 0
	for _, tracker := range p.offsetTrackers {
		jobsQueued += tracker.GetInflightCount()
	}
	p.trackersMu.RUnlock()

	// Get worker stats from worker pool
	workersActive = p.config.NumWorkers // Active workers (simplified - all workers are always active)

	return Stats{
		MessagesProcessed: atomic.LoadInt64(&p.statsMessagesProcessed),
		MessagesFailed:    atomic.LoadInt64(&p.statsMessagesFailed),
		OffsetsCommitted:  atomic.LoadInt64(&p.statsOffsetsCommitted),
		WorkersActive:     workersActive,
		JobsQueued:        jobsQueued,
	}
}
