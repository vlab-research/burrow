// commit.go
package burrow

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/confluentinc/confluent-kafka-go/v2/kafka"
	"go.uber.org/zap"
)

// CommitManager handles periodic offset commits
type CommitManager struct {
	consumer            *kafka.Consumer
	tracker             *SequenceTracker // Single tracker for all partitions
	commitInterval      time.Duration
	commitBatchSize     int
	messagesSinceCommit int64
	logger              *zap.Logger
	statsCounter        *int64 // Pointer to pool's stats counter
}

// NewCommitManager creates a new commit manager
func NewCommitManager(consumer *kafka.Consumer, tracker *SequenceTracker, commitInterval time.Duration, commitBatchSize int, logger *zap.Logger, statsCounter *int64) *CommitManager {
	return &CommitManager{
		consumer:        consumer,
		tracker:         tracker,
		commitInterval:  commitInterval,
		commitBatchSize: commitBatchSize,
		logger:          logger,
		statsCounter:    statsCounter,
	}
}

// RecordMessage increments message counter (for batch commit trigger)
func (cm *CommitManager) RecordMessage() {
	atomic.AddInt64(&cm.messagesSinceCommit, 1)
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
		}

		// Check batch size after each commit
		if atomic.LoadInt64(&cm.messagesSinceCommit) >= int64(cm.commitBatchSize) {
			cm.tryCommit(ctx)
		}
	}
}

// tryCommit attempts to commit offsets for all partitions
func (cm *CommitManager) tryCommit(ctx context.Context) error {
	// Get committable offsets from the sequence tracker
	offsetsByPartition := cm.tracker.GetCommittableOffsets()

	if len(offsetsByPartition) == 0 {
		cm.logger.Debug("no offsets to commit")
		return nil
	}

	// Build Kafka commit payload
	// Kafka expects "next offset to read", so we add 1 to the message offset
	offsets := make([]kafka.TopicPartition, 0, len(offsetsByPartition))
	for partition, offset := range offsetsByPartition {
		offsets = append(offsets, kafka.TopicPartition{
			Partition: partition,
			Offset:    kafka.Offset(offset + 1),
		})
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

	// Update tracker with committed sequence
	committableSeq := cm.tracker.GetCommittableSequence()
	cm.tracker.CommitSequence(committableSeq)

	// Reset message counter
	atomic.StoreInt64(&cm.messagesSinceCommit, 0)

	// Update stats counter
	if cm.statsCounter != nil {
		atomic.AddInt64(cm.statsCounter, int64(len(offsets)))
	}

	cm.logger.Info("successfully committed offsets",
		zap.Int("partitions", len(offsets)))

	return nil
}
