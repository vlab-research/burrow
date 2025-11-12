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
	consumer        *kafka.Consumer
	config          Config
	sequenceTracker *SequenceTracker
	processFunc     ProcessFunc
	logger          *zap.Logger

	// Channels
	jobs    chan *Job
	results chan *Result

	// Lifecycle
	ctx        context.Context
	cancel     context.CancelFunc
	pollCtx    context.Context
	pollCancel context.CancelFunc
	wg         sync.WaitGroup

	rebalanceCb kafka.RebalanceCb

	// Commit tracking
	messagesSinceCommit int64

	// Statistics
	statsMessagesProcessed int64
	statsMessagesFailed    int64
	statsOffsetsCommitted  int64
}

// NewPool creates a new Burrow pool
func NewPool(consumer *kafka.Consumer, config Config) (*Pool, error) {
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	pollCtx, pollCancel := context.WithCancel(context.Background())

	pool := &Pool{
		consumer:        consumer,
		config:          config,
		sequenceTracker: NewSequenceTracker(config.Logger),
		logger:          config.Logger,
		jobs:            make(chan *Job, config.JobQueueSize),
		results:         make(chan *Result, config.ResultQueueSize),
		ctx:             ctx,
		cancel:          cancel,
		pollCtx:         pollCtx,
		pollCancel:      pollCancel,
	}

	pool.setupRebalanceCallback()
	return pool, nil
}

// Run starts the pool and processes messages until context is cancelled
func (p *Pool) Run(ctx context.Context, processFunc ProcessFunc) error {
	p.processFunc = processFunc
	p.logger.Info("starting pool",
		zap.Int("num_workers", p.config.NumWorkers),
		zap.Duration("commit_interval", p.config.CommitInterval))

	// Start workers
	for i := 0; i < p.config.NumWorkers; i++ {
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			p.worker()
		}()
	}

	// Start commit loop
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.commitLoop()
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
	close(p.jobs)
	p.wg.Wait()

	return err
}

// worker processes jobs from the jobs channel
func (p *Pool) worker() {
	for {
		select {
		case <-p.ctx.Done():
			return
		case job := <-p.jobs:
			if job == nil {
				return
			}
			err := p.processFunc(p.ctx, job.Message)
			p.results <- &Result{
				Sequence: job.Sequence,
				Error:    err,
			}
		}
	}
}

// pollLoop reads messages from Kafka and dispatches to workers
func (p *Pool) pollLoop(ctx context.Context, processFunc ProcessFunc) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-p.pollCtx.Done():
			// Poll context cancelled (FreezeOnError stopped polling)
			p.logger.Info("poll loop stopped due to error freeze")
			return p.pollCtx.Err()

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

			// Assign sequence number
			sequence := p.sequenceTracker.AssignSequence(
				msg.TopicPartition.Partition,
				int64(msg.TopicPartition.Offset),
			)

			// Create job
			job := &Job{
				Sequence: sequence,
				Message:  msg,
			}

			// Record inflight with sequence
			p.sequenceTracker.RecordInflight(sequence)

			// Submit to workers (blocks if queue is full - backpressure!)
			select {
			case p.jobs <- job:
			case <-ctx.Done():
				return ctx.Err()
			}

			// Record message for batch commit trigger
			atomic.AddInt64(&p.messagesSinceCommit, 1)
		}
	}
}

// processResults handles results from workers
func (p *Pool) processResults() {
	for result := range p.results {
		if result.Error == nil {
			// Success!
			p.sequenceTracker.MarkProcessed(result.Sequence)
			atomic.AddInt64(&p.statsMessagesProcessed, 1)
		} else {
			// Failure - app returned error, something is seriously wrong
			p.handleError(result)
			return // Stop processing results
		}
	}
}

// handleError handles a processing error based on configured behavior
func (p *Pool) handleError(result *Result) {
	p.logger.Error("message processing failed",
		zap.Int64("sequence", result.Sequence),
		zap.Error(result.Error))

	atomic.AddInt64(&p.statsMessagesFailed, 1)

	switch p.config.OnError {
	case FatalOnError:
		// Blow up immediately - no graceful shutdown
		p.logger.Fatal("exiting immediately due to processing error",
			zap.Int64("sequence", result.Sequence),
			zap.Error(result.Error))
		// Fatal calls os.Exit(1)

	case FreezeOnError:
		// Mark as failed (creates gap, blocks commits past this point)
		p.sequenceTracker.MarkFailed(result.Sequence)

		// Stop polling new messages
		p.pollCancel()

		// Wait for inflight to complete
		p.logger.Info("waiting for inflight messages to complete")
		err := p.sequenceTracker.WaitForInflight(context.Background(), p.config.ShutdownTimeout)
		if err != nil {
			p.logger.Error("timeout waiting for inflight", zap.Error(err))
		}

		// Final commit (up to the gap)
		p.commit(context.Background())

		p.logger.Error("consumer frozen due to processing error - manual restart required",
			zap.Int64("failed_sequence", result.Sequence),
			zap.Error(result.Error))

		// Freeze forever (block until external signal)
		select {}
	}
}

// commitLoop runs periodic commits
func (p *Pool) commitLoop() {
	ticker := time.NewTicker(p.config.CommitInterval)
	defer ticker.Stop()

	for {
		select {
		case <-p.ctx.Done():
			p.commit(context.Background()) // Final commit
			return
		case <-ticker.C:
			p.commit(p.ctx)
		}

		// Check batch size trigger
		if atomic.LoadInt64(&p.messagesSinceCommit) >= int64(p.config.CommitBatchSize) {
			p.commit(p.ctx)
		}
	}
}

// commit attempts to commit offsets
func (p *Pool) commit(ctx context.Context) error {
	offsetsByPartition := p.sequenceTracker.GetCommittableOffsets()
	if len(offsetsByPartition) == 0 {
		return nil
	}

	// Build Kafka commit payload (Kafka expects "next offset to read", so add 1)
	offsets := make([]kafka.TopicPartition, 0, len(offsetsByPartition))
	for partition, offset := range offsetsByPartition {
		offsets = append(offsets, kafka.TopicPartition{
			Partition: partition,
			Offset:    kafka.Offset(offset + 1),
		})
	}

	// Synchronous commit for safety
	_, err := p.consumer.CommitOffsets(offsets)
	if err != nil {
		p.logger.Error("failed to commit offsets", zap.Error(err))
		return err
	}

	// Update tracker with committed sequence
	committableSeq := p.sequenceTracker.GetCommittableSequence()
	p.sequenceTracker.CommitSequence(committableSeq)

	// Reset counter and update stats
	atomic.StoreInt64(&p.messagesSinceCommit, 0)
	atomic.AddInt64(&p.statsOffsetsCommitted, int64(len(offsets)))

	p.logger.Info("committed offsets", zap.Int("partitions", len(offsets)))
	return nil
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

	// Sequence tracker is global - no per-partition setup needed
}

// onPartitionsRevoked handles partition revocation
func (p *Pool) onPartitionsRevoked(partitions []kafka.TopicPartition) {
	p.logger.Info("partitions revoked",
		zap.Int("count", len(partitions)),
		zap.Any("partitions", partitions))

	// Wait for ALL inflight messages to complete (simplified from per-partition wait)
	inflight := p.sequenceTracker.GetInflightCount()
	if inflight > 0 {
		p.logger.Info("waiting for inflight messages",
			zap.Int("inflight", inflight))

		// Wait up to 30 seconds for all inflight to complete
		if err := p.sequenceTracker.WaitForInflight(p.ctx, 30*time.Second); err != nil {
			p.logger.Error("failed to wait for inflight messages", zap.Error(err))
			// Continue anyway - we tried our best
		}
	}

	// Final commit before losing partitions
	p.commit(p.ctx)

	p.logger.Info("partitions cleanup complete", zap.Int("count", len(partitions)))
}

// GetStats returns runtime statistics
func (p *Pool) GetStats() Stats {
	return Stats{
		MessagesProcessed: atomic.LoadInt64(&p.statsMessagesProcessed),
		MessagesFailed:    atomic.LoadInt64(&p.statsMessagesFailed),
		OffsetsCommitted:  atomic.LoadInt64(&p.statsOffsetsCommitted),
		WorkersActive:     p.config.NumWorkers,
		JobsQueued:        p.sequenceTracker.GetInflightCount(),
	}
}
