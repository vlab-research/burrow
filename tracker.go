// tracker.go
package burrow

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
)

// OffsetTracker tracks offset processing status for a single partition
type OffsetTracker struct {
	mu            sync.Mutex
	partition     int32
	processedMap  map[int64]bool // offset -> completed?
	inflightMap   map[int64]bool // offset -> currently processing?
	lastCommitted int64           // Last offset committed to Kafka
	highWatermark int64           // Highest offset we've seen
	logger        *zap.Logger
}

// NewOffsetTracker creates a new tracker for a partition
func NewOffsetTracker(partition int32, logger *zap.Logger) *OffsetTracker {
	return &OffsetTracker{
		partition:     partition,
		processedMap:  make(map[int64]bool),
		inflightMap:   make(map[int64]bool),
		lastCommitted: -1, // No offsets committed yet
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
// Uses the pure function FindCommittableOffset for the core logic
func (ot *OffsetTracker) GetCommittableOffset() int64 {
	ot.mu.Lock()
	defer ot.mu.Unlock()

	// Use pure function for gap detection logic
	return FindCommittableOffset(ot.processedMap, ot.lastCommitted, ot.highWatermark)
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
// Returns error if timeout exceeded or context cancelled
func (ot *OffsetTracker) WaitForInflight(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	// Log once at start if there are inflight messages
	initialCount := ot.GetInflightCount()
	if initialCount > 0 {
		ot.logger.Info("waiting for inflight messages to complete",
			zap.Int("inflight", initialCount))
	}

	for {
		count := ot.GetInflightCount()
		if count == 0 {
			return nil
		}

		if time.Now().After(deadline) {
			ot.logger.Error("timeout waiting for inflight messages",
				zap.Int("remaining", count),
				zap.Duration("timeout", timeout))
			return fmt.Errorf("timeout waiting for %d inflight messages", count)
		}

		select {
		case <-ctx.Done():
			ot.logger.Warn("context cancelled while waiting for inflight messages",
				zap.Int("remaining", count))
			return ctx.Err()
		case <-ticker.C:
			// Only log every second (10 ticks) to reduce spam
			// Continue waiting
		}
	}
}
