// commit.go
package burrow

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/confluentinc/confluent-kafka-go/v2/kafka"
	"go.uber.org/zap"
)

// CommitManager handles periodic offset commits
type CommitManager struct {
	consumer            *kafka.Consumer
	trackers            map[int32]*OffsetTracker
	trackersMu          sync.RWMutex
	commitInterval      time.Duration
	commitBatchSize     int
	messagesSinceCommit int64
	logger              *zap.Logger
	statsCounter        *int64 // Pointer to pool's stats counter
}

// NewCommitManager creates a new commit manager
func NewCommitManager(consumer *kafka.Consumer, commitInterval time.Duration, commitBatchSize int, logger *zap.Logger, statsCounter *int64) *CommitManager {
	return &CommitManager{
		consumer:        consumer,
		trackers:        make(map[int32]*OffsetTracker),
		commitInterval:  commitInterval,
		commitBatchSize: commitBatchSize,
		logger:          logger,
		statsCounter:    statsCounter,
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
	cm.trackersMu.RLock()
	defer cm.trackersMu.RUnlock()

	if len(cm.trackers) == 0 {
		return nil // No partitions assigned
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
		tracker.CommitOffset(int64(tp.Offset) - 1) // Convert back to message offset
	}

	// Reset counter
	atomic.StoreInt64(&cm.messagesSinceCommit, 0)

	// Update stats counter
	if cm.statsCounter != nil {
		atomic.AddInt64(cm.statsCounter, int64(len(offsets)))
	}

	cm.logger.Info("successfully committed offsets",
		zap.Int("partitions", len(offsets)))

	return nil
}
