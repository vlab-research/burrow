package burrow_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/vlab-research/fly/burrow"
	"go.uber.org/zap"
)

func TestOffsetTracker_GetCommittableOffset(t *testing.T) {
	tests := []struct {
		name      string
		processed []int64
		want      int64
	}{
		{
			name:      "all processed in order",
			processed: []int64{0, 1, 2, 3, 4},
			want:      4,
		},
		{
			name:      "gap at offset 2",
			processed: []int64{0, 1, 3, 4, 5},
			want:      1, // Can only commit up to 1 (gap at 2)
		},
		{
			name:      "gap at beginning",
			processed: []int64{1, 2, 3, 4},
			want:      -1, // Can't commit anything (missing 0)
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
		{
			name:      "gap in middle",
			processed: []int64{0, 1, 2, 4, 5, 6},
			want:      2, // Can only commit up to 2 (gap at 3)
		},
		{
			name:      "multiple gaps",
			processed: []int64{0, 2, 4, 6, 8},
			want:      0, // Can only commit 0 (gap at 1)
		},
		{
			name:      "large contiguous range",
			processed: []int64{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15},
			want:      15,
		},
		{
			name:      "out of order processing",
			processed: []int64{5, 2, 0, 3, 1, 4},
			want:      5, // All processed, should commit up to 5
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, _ := zap.NewDevelopment()
			tracker := burrow.NewOffsetTracker(0, logger)

			// Mark offsets as processed
			for _, offset := range tt.processed {
				tracker.RecordInflight(offset)
				tracker.MarkProcessed(offset)
			}

			got := tracker.GetCommittableOffset()
			assert.Equal(t, tt.want, got, "GetCommittableOffset() = %d, want %d", got, tt.want)
		})
	}
}

func TestOffsetTracker_MarkFailed(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	tracker := burrow.NewOffsetTracker(0, logger)

	// Process offsets 0, 1, 2
	tracker.RecordInflight(0)
	tracker.MarkProcessed(0)
	tracker.RecordInflight(1)
	tracker.MarkProcessed(1)
	tracker.RecordInflight(2)
	tracker.MarkFailed(2) // Fail offset 2

	// Process offsets 3, 4
	tracker.RecordInflight(3)
	tracker.MarkProcessed(3)
	tracker.RecordInflight(4)
	tracker.MarkProcessed(4)

	// Should only be able to commit up to 1 (gap at 2)
	committable := tracker.GetCommittableOffset()
	assert.Equal(t, int64(1), committable, "Expected committable offset 1 due to failure at 2")
}

func TestOffsetTracker_CommitOffset(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	tracker := burrow.NewOffsetTracker(0, logger)

	// Process offsets 0-9
	for i := int64(0); i < 10; i++ {
		tracker.RecordInflight(i)
		tracker.MarkProcessed(i)
	}

	// Commit offset 5
	tracker.CommitOffset(5)
	assert.Equal(t, int64(5), tracker.GetLastCommitted())

	// After commit, should be able to continue from 6
	committable := tracker.GetCommittableOffset()
	assert.Equal(t, int64(9), committable, "Should be able to commit up to 9")
}

func TestOffsetTracker_GetInflightCount(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	tracker := burrow.NewOffsetTracker(0, logger)

	// No inflight initially
	assert.Equal(t, 0, tracker.GetInflightCount())

	// Mark 3 offsets as inflight
	tracker.RecordInflight(0)
	tracker.RecordInflight(1)
	tracker.RecordInflight(2)
	assert.Equal(t, 3, tracker.GetInflightCount())

	// Complete one
	tracker.MarkProcessed(1)
	assert.Equal(t, 2, tracker.GetInflightCount())

	// Fail one
	tracker.MarkFailed(2)
	assert.Equal(t, 1, tracker.GetInflightCount())

	// Complete last one
	tracker.MarkProcessed(0)
	assert.Equal(t, 0, tracker.GetInflightCount())
}

func TestOffsetTracker_MemoryLeak(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	tracker := burrow.NewOffsetTracker(0, logger)

	// Process many offsets
	for i := int64(0); i < 1000; i++ {
		tracker.RecordInflight(i)
		tracker.MarkProcessed(i)
	}

	// Commit up to 990
	tracker.CommitOffset(990)

	// Now process more
	for i := int64(1000); i < 2000; i++ {
		tracker.RecordInflight(i)
		tracker.MarkProcessed(i)
	}

	committable := tracker.GetCommittableOffset()
	assert.Equal(t, int64(1999), committable)
}

func TestOffsetTracker_ConcurrentAccess(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	tracker := burrow.NewOffsetTracker(0, logger)

	// Simulate concurrent processing
	done := make(chan bool)
	numGoroutines := 10
	offsetsPerGoroutine := 100

	for g := 0; g < numGoroutines; g++ {
		go func(goroutineID int) {
			startOffset := int64(goroutineID * offsetsPerGoroutine)
			for i := int64(0); i < int64(offsetsPerGoroutine); i++ {
				offset := startOffset + i
				tracker.RecordInflight(offset)
				tracker.MarkProcessed(offset)
			}
			done <- true
		}(g)
	}

	// Wait for all goroutines
	for g := 0; g < numGoroutines; g++ {
		<-done
	}

	// All offsets should be processed
	committable := tracker.GetCommittableOffset()
	expectedMax := int64(numGoroutines*offsetsPerGoroutine - 1)
	assert.Equal(t, expectedMax, committable)
}

func TestOffsetTracker_GapAfterCommit(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	tracker := burrow.NewOffsetTracker(0, logger)

	// Process and commit 0-4
	for i := int64(0); i < 5; i++ {
		tracker.RecordInflight(i)
		tracker.MarkProcessed(i)
	}
	tracker.CommitOffset(4)

	// Now process 5, skip 6, process 7-9
	tracker.RecordInflight(5)
	tracker.MarkProcessed(5)
	tracker.RecordInflight(6)
	tracker.MarkFailed(6) // Create gap
	for i := int64(7); i < 10; i++ {
		tracker.RecordInflight(i)
		tracker.MarkProcessed(i)
	}

	// Should only be able to commit up to 5
	committable := tracker.GetCommittableOffset()
	assert.Equal(t, int64(5), committable, "Should stop at gap at offset 6")
}

func TestOffsetTracker_HighWatermark(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	tracker := burrow.NewOffsetTracker(0, logger)

	// Record offsets out of order
	tracker.RecordInflight(5)
	tracker.RecordInflight(2)
	tracker.RecordInflight(8)
	tracker.RecordInflight(1)

	// Mark some as processed (but with gaps)
	tracker.MarkProcessed(1)
	tracker.MarkProcessed(2)
	tracker.MarkProcessed(5)
	tracker.MarkProcessed(8)

	// Should not be able to commit anything (missing 0)
	committable := tracker.GetCommittableOffset()
	assert.Equal(t, int64(-1), committable, "Should not commit with gap at beginning")
}

func TestOffsetTracker_EdgeCases(t *testing.T) {
	t.Run("negative offsets not supported", func(t *testing.T) {
		logger, _ := zap.NewDevelopment()
		tracker := burrow.NewOffsetTracker(0, logger)

		// Process starting from 0
		tracker.RecordInflight(0)
		tracker.MarkProcessed(0)

		committable := tracker.GetCommittableOffset()
		assert.Equal(t, int64(0), committable)
	})

	t.Run("very large offsets", func(t *testing.T) {
		logger, _ := zap.NewDevelopment()
		tracker := burrow.NewOffsetTracker(0, logger)

		largeOffset := int64(1000000)
		tracker.RecordInflight(largeOffset)
		tracker.MarkProcessed(largeOffset)

		// Can't commit because there's a gap from -1 to 1000000
		committable := tracker.GetCommittableOffset()
		assert.Equal(t, int64(-1), committable)
	})

	t.Run("duplicate processing", func(t *testing.T) {
		logger, _ := zap.NewDevelopment()
		tracker := burrow.NewOffsetTracker(0, logger)

		// Process same offset twice
		tracker.RecordInflight(0)
		tracker.RecordInflight(0) // Duplicate
		tracker.MarkProcessed(0)
		tracker.MarkProcessed(0) // Duplicate

		tracker.RecordInflight(1)
		tracker.MarkProcessed(1)

		committable := tracker.GetCommittableOffset()
		assert.Equal(t, int64(1), committable)
	})
}
