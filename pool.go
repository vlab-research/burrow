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
	workerPool      *WorkerPool
	commitManager   *CommitManager
	sequenceTracker *SequenceTracker // Single tracker for all partitions
	logger          *zap.Logger
	ctx             context.Context
	cancel          context.CancelFunc
	pollCtx         context.Context      // Context for poll loop
	pollCancel      context.CancelFunc   // Cancel function to stop polling
	wg              sync.WaitGroup
	rebalanceCb     kafka.RebalanceCb

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
	pollCtx, pollCancel := context.WithCancel(context.Background())

	// Create worker pool
	workerPool := NewWorkerPool(
		config.NumWorkers,
		config.JobQueueSize,
		config.ResultQueueSize,
		config.Logger,
	)

	// Create sequence tracker
	sequenceTracker := NewSequenceTracker(config.Logger)

	pool := &Pool{
		consumer:        consumer,
		config:          config,
		workerPool:      workerPool,
		sequenceTracker: sequenceTracker,
		logger:          config.Logger,
		ctx:             ctx,
		cancel:          cancel,
		pollCtx:         pollCtx,
		pollCancel:      pollCancel,
	}

	// Create commit manager with sequence tracker
	commitManager := NewCommitManager(
		consumer,
		sequenceTracker,
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
				Sequence:    sequence,
				Partition:   msg.TopicPartition.Partition,
				Offset:      int64(msg.TopicPartition.Offset),
				Message:     msg,
				ProcessFunc: processFunc,
			}

			// Record inflight with sequence
			p.sequenceTracker.RecordInflight(sequence)

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
		if result.Success {
			// Success!
			p.sequenceTracker.MarkProcessed(result.Sequence)
			atomic.AddInt64(&p.statsMessagesProcessed, 1)

			p.logger.Debug("message processed successfully",
				zap.Int64("sequence", result.Sequence),
				zap.Int32("partition", result.Partition),
				zap.Int64("offset", result.Offset))

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
		zap.Int32("partition", result.Partition),
		zap.Int64("offset", result.Offset),
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
		p.commitManager.tryCommit(context.Background())

		p.logger.Error("consumer frozen due to processing error - manual restart required",
			zap.Int64("failed_sequence", result.Sequence),
			zap.Error(result.Error))

		// Freeze forever (block until external signal)
		select {}
	}
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
	p.commitManager.tryCommit(p.ctx)

	p.logger.Info("partitions cleanup complete",
		zap.Int("count", len(partitions)))
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
