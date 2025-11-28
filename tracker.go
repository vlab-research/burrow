// tracker.go
package burrow

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// SequenceTracker tracks message processing using global sequence numbers
// This replaces the per-partition OffsetTracker approach with a simpler
// sequential tracking model that assigns monotonic sequence numbers to messages
// as they arrive, regardless of partition.
type SequenceTracker struct {
	mu            sync.Mutex
	messages      map[int64]MessageInfo // sequence → partition/offset mapping
	processedMap  map[int64]bool        // sequence → completed?
	inflightMap   map[int64]bool        // sequence → currently processing?
	nextSequence  int64                 // atomic counter for sequence assignment
	lastCommitted int64                 // last committed sequence
	highWatermark int64                 // highest sequence seen
	logger        *zap.Logger
}

// NewSequenceTracker creates a new sequence tracker
func NewSequenceTracker(logger *zap.Logger) *SequenceTracker {
	return &SequenceTracker{
		messages:      make(map[int64]MessageInfo),
		processedMap:  make(map[int64]bool),
		inflightMap:   make(map[int64]bool),
		nextSequence:  0,
		lastCommitted: -1, // No sequences committed yet
		highWatermark: -1,
		logger:        logger,
	}
}

// AssignSequence assigns the next sequence number to a message
// This method is thread-safe and uses atomic operations for the counter
func (st *SequenceTracker) AssignSequence(topic *string, partition int32, offset int64) int64 {
	// Atomically increment and get the next sequence number
	seq := atomic.AddInt64(&st.nextSequence, 1) - 1

	st.mu.Lock()
	defer st.mu.Unlock()

	// Store the topic/partition/offset mapping for this sequence
	st.messages[seq] = MessageInfo{
		Topic:     topic,
		Partition: partition,
		Offset:    offset,
	}

	// Update high watermark
	if seq > st.highWatermark {
		st.highWatermark = seq
	}

	return seq
}

// RecordInflight marks a sequence as currently being processed
func (st *SequenceTracker) RecordInflight(seq int64) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.inflightMap[seq] = true
}

// MarkProcessed marks a sequence as successfully completed
func (st *SequenceTracker) MarkProcessed(seq int64) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.processedMap[seq] = true
	delete(st.inflightMap, seq)
}

// MarkFailed marks a sequence as failed (leaves a gap for at-least-once semantics)
func (st *SequenceTracker) MarkFailed(seq int64) {
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.inflightMap, seq) // Don't mark as processed - creates a gap
}

// GetCommittableSequence returns the highest sequence that can be safely committed
// Uses the pure gap detection algorithm from gap.go
func (st *SequenceTracker) GetCommittableSequence() int64 {
	st.mu.Lock()
	defer st.mu.Unlock()

	// Reuse the pure gap detection function
	return FindCommittableOffset(st.processedMap, st.lastCommitted, st.highWatermark)
}

// GetCommittableOffsets converts the committable sequence range into
// MessageInfo suitable for Kafka commit.
// This aggregates all committable sequences and returns the highest offset
// per partition that can be safely committed.
func (st *SequenceTracker) GetCommittableOffsets() []MessageInfo {
	st.mu.Lock()
	defer st.mu.Unlock()

	// Find the highest committable sequence
	committableSeq := FindCommittableOffset(st.processedMap, st.lastCommitted, st.highWatermark)

	// No progress since last commit
	if committableSeq <= st.lastCommitted {
		return nil
	}

	// Convert sequence range to highest offset per partition
	// Key by partition since we can have messages from different partitions
	offsetsByPartition := make(map[int32]MessageInfo)

	for seq := st.lastCommitted + 1; seq <= committableSeq; seq++ {
		if info, exists := st.messages[seq]; exists {
			// Track the highest offset for each partition
			if current, ok := offsetsByPartition[info.Partition]; !ok || info.Offset > current.Offset {
				offsetsByPartition[info.Partition] = info
			}
		}
	}

	// Convert map to slice
	result := make([]MessageInfo, 0, len(offsetsByPartition))
	for _, info := range offsetsByPartition {
		result = append(result, info)
	}

	return result
}

// CommitSequence updates the last committed sequence and cleans up old data
func (st *SequenceTracker) CommitSequence(seq int64) {
	st.mu.Lock()
	defer st.mu.Unlock()

	st.lastCommitted = seq

	// Clean up old entries to prevent unbounded memory growth
	for s := range st.processedMap {
		if s <= seq {
			delete(st.processedMap, s)
			delete(st.messages, s)
		}
	}
}

// GetInflightCount returns the number of messages currently being processed
func (st *SequenceTracker) GetInflightCount() int {
	st.mu.Lock()
	defer st.mu.Unlock()
	return len(st.inflightMap)
}

// GetLastCommitted returns the last committed sequence number
func (st *SequenceTracker) GetLastCommitted() int64 {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.lastCommitted
}

// WaitForInflight blocks until all inflight messages complete or timeout is reached
// This is used during graceful shutdown and partition rebalancing
func (st *SequenceTracker) WaitForInflight(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	// Log once at start if there are inflight messages
	initialCount := st.GetInflightCount()
	if initialCount > 0 {
		st.logger.Info("waiting for inflight messages to complete",
			zap.Int("inflight", initialCount))
	}

	for {
		count := st.GetInflightCount()
		if count == 0 {
			return nil
		}

		if time.Now().After(deadline) {
			st.logger.Error("timeout waiting for inflight messages",
				zap.Int("remaining", count),
				zap.Duration("timeout", timeout))
			return fmt.Errorf("timeout waiting for %d inflight messages", count)
		}

		select {
		case <-ctx.Done():
			st.logger.Warn("context cancelled while waiting for inflight messages",
				zap.Int("remaining", count))
			return ctx.Err()
		case <-ticker.C:
			// Continue waiting
		}
	}
}
