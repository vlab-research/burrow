package burrow_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/vlab-research/fly/burrow"
	"go.uber.org/zap"
)

func TestSequenceTracker_GetCommittableSequence(t *testing.T) {
	tests := []struct {
		name      string
		sequences []int64
		want      int64
	}{
		{
			name:      "all processed in order",
			sequences: []int64{0, 1, 2, 3, 4},
			want:      4,
		},
		{
			name:      "gap at sequence 2",
			sequences: []int64{0, 1, 3, 4, 5},
			want:      1, // Can only commit up to 1 (gap at 2)
		},
		{
			name:      "gap at beginning",
			sequences: []int64{1, 2, 3, 4},
			want:      -1, // Can't commit anything (missing 0)
		},
		{
			name:      "single message",
			sequences: []int64{0},
			want:      0,
		},
		{
			name:      "empty",
			sequences: []int64{},
			want:      -1,
		},
		{
			name:      "gap in middle",
			sequences: []int64{0, 1, 2, 4, 5, 6},
			want:      2, // Can only commit up to 2 (gap at 3)
		},
		{
			name:      "multiple gaps",
			sequences: []int64{0, 2, 4, 6, 8},
			want:      0, // Can only commit 0 (gap at 1)
		},
		{
			name:      "large contiguous range",
			sequences: []int64{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15},
			want:      15,
		},
		{
			name:      "out of order processing",
			sequences: []int64{5, 2, 0, 3, 1, 4},
			want:      5, // All processed, should commit up to 5
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, _ := zap.NewDevelopment()
			tracker := burrow.NewSequenceTracker(logger)

			// Assign sequences and mark as processed
			for _, seq := range tt.sequences {
				// Need to assign sequence first (simulating arrival)
				// For testing, we'll manually add to maps
				tracker.AssignSequence(0, seq) // partition 0, offset = seq for simplicity
				tracker.RecordInflight(seq)
				tracker.MarkProcessed(seq)
			}

			got := tracker.GetCommittableSequence()
			assert.Equal(t, tt.want, got, "GetCommittableSequence() = %d, want %d", got, tt.want)
		})
	}
}

func TestSequenceTracker_MarkFailed(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	tracker := burrow.NewSequenceTracker(logger)

	// Process sequences 0, 1, 2
	seq0 := tracker.AssignSequence(0, 100)
	tracker.RecordInflight(seq0)
	tracker.MarkProcessed(seq0)

	seq1 := tracker.AssignSequence(0, 101)
	tracker.RecordInflight(seq1)
	tracker.MarkProcessed(seq1)

	seq2 := tracker.AssignSequence(0, 102)
	tracker.RecordInflight(seq2)
	tracker.MarkFailed(seq2) // Fail sequence 2

	// Process sequences 3, 4
	seq3 := tracker.AssignSequence(0, 103)
	tracker.RecordInflight(seq3)
	tracker.MarkProcessed(seq3)

	seq4 := tracker.AssignSequence(0, 104)
	tracker.RecordInflight(seq4)
	tracker.MarkProcessed(seq4)

	// Should only be able to commit up to 1 (gap at 2)
	committable := tracker.GetCommittableSequence()
	assert.Equal(t, int64(1), committable, "Expected committable sequence 1 due to failure at 2")
}

func TestSequenceTracker_CommitSequence(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	tracker := burrow.NewSequenceTracker(logger)

	// Process sequences 0-9
	for i := int64(0); i < 10; i++ {
		seq := tracker.AssignSequence(0, 100+i)
		tracker.RecordInflight(seq)
		tracker.MarkProcessed(seq)
	}

	// Commit sequence 5
	tracker.CommitSequence(5)
	assert.Equal(t, int64(5), tracker.GetLastCommitted())

	// After commit, should be able to continue from 6
	committable := tracker.GetCommittableSequence()
	assert.Equal(t, int64(9), committable, "Should be able to commit up to 9")
}

func TestSequenceTracker_GetInflightCount(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	tracker := burrow.NewSequenceTracker(logger)

	// No inflight initially
	assert.Equal(t, 0, tracker.GetInflightCount())

	// Assign and mark 3 sequences as inflight
	seq0 := tracker.AssignSequence(0, 100)
	tracker.RecordInflight(seq0)

	seq1 := tracker.AssignSequence(0, 101)
	tracker.RecordInflight(seq1)

	seq2 := tracker.AssignSequence(0, 102)
	tracker.RecordInflight(seq2)

	assert.Equal(t, 3, tracker.GetInflightCount())

	// Complete one
	tracker.MarkProcessed(seq1)
	assert.Equal(t, 2, tracker.GetInflightCount())

	// Fail one
	tracker.MarkFailed(seq2)
	assert.Equal(t, 1, tracker.GetInflightCount())

	// Complete last one
	tracker.MarkProcessed(seq0)
	assert.Equal(t, 0, tracker.GetInflightCount())
}

func TestSequenceTracker_MemoryLeak(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	tracker := burrow.NewSequenceTracker(logger)

	// Process many sequences
	for i := int64(0); i < 1000; i++ {
		seq := tracker.AssignSequence(0, 100+i)
		tracker.RecordInflight(seq)
		tracker.MarkProcessed(seq)
	}

	// Commit up to 990
	tracker.CommitSequence(990)

	// Now process more
	for i := int64(1000); i < 2000; i++ {
		seq := tracker.AssignSequence(0, 100+i)
		tracker.RecordInflight(seq)
		tracker.MarkProcessed(seq)
	}

	committable := tracker.GetCommittableSequence()
	assert.Equal(t, int64(1999), committable)
}

func TestSequenceTracker_ConcurrentAccess(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	tracker := burrow.NewSequenceTracker(logger)

	// Simulate concurrent processing
	done := make(chan bool)
	numGoroutines := 10
	sequencesPerGoroutine := 100

	for g := 0; g < numGoroutines; g++ {
		go func(goroutineID int) {
			for i := 0; i < sequencesPerGoroutine; i++ {
				seq := tracker.AssignSequence(0, int64(goroutineID*1000+i))
				tracker.RecordInflight(seq)
				tracker.MarkProcessed(seq)
			}
			done <- true
		}(g)
	}

	// Wait for all goroutines
	for g := 0; g < numGoroutines; g++ {
		<-done
	}

	// All sequences should be processed
	committable := tracker.GetCommittableSequence()
	expectedMax := int64(numGoroutines*sequencesPerGoroutine - 1)
	assert.Equal(t, expectedMax, committable)
}

func TestSequenceTracker_GapAfterCommit(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	tracker := burrow.NewSequenceTracker(logger)

	// Process and commit 0-4
	for i := int64(0); i < 5; i++ {
		seq := tracker.AssignSequence(0, 100+i)
		tracker.RecordInflight(seq)
		tracker.MarkProcessed(seq)
	}
	tracker.CommitSequence(4)

	// Now process 5, skip 6, process 7-9
	seq5 := tracker.AssignSequence(0, 105)
	tracker.RecordInflight(seq5)
	tracker.MarkProcessed(seq5)

	seq6 := tracker.AssignSequence(0, 106)
	tracker.RecordInflight(seq6)
	tracker.MarkFailed(seq6) // Create gap

	for i := int64(7); i < 10; i++ {
		seq := tracker.AssignSequence(0, 100+i)
		tracker.RecordInflight(seq)
		tracker.MarkProcessed(seq)
	}

	// Should only be able to commit up to 5
	committable := tracker.GetCommittableSequence()
	assert.Equal(t, int64(5), committable, "Should stop at gap at sequence 6")
}

func TestSequenceTracker_GetCommittableOffsets(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	tracker := burrow.NewSequenceTracker(logger)

	// Interleave messages from two partitions
	seq0 := tracker.AssignSequence(0, 100) // P0-O100
	seq1 := tracker.AssignSequence(1, 200) // P1-O200
	seq2 := tracker.AssignSequence(0, 101) // P0-O101
	seq3 := tracker.AssignSequence(1, 201) // P1-O201

	// Mark all as processed
	tracker.MarkProcessed(seq0)
	tracker.MarkProcessed(seq1)
	tracker.MarkProcessed(seq2)
	tracker.MarkProcessed(seq3)

	// Get committable offsets
	offsets := tracker.GetCommittableOffsets()

	// Should return highest offset per partition
	assert.Equal(t, int64(101), offsets[0], "Partition 0 should commit up to 101")
	assert.Equal(t, int64(201), offsets[1], "Partition 1 should commit up to 201")
}

func TestSequenceTracker_MultiPartitionBlocking(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	tracker := burrow.NewSequenceTracker(logger)

	// Interleaved partitions
	seq0 := tracker.AssignSequence(0, 100) // P0-O100
	seq1 := tracker.AssignSequence(1, 200) // P1-O200
	seq2 := tracker.AssignSequence(0, 101) // P0-O101
	seq3 := tracker.AssignSequence(1, 201) // P1-O201 (will fail)

	tracker.MarkProcessed(seq0)
	tracker.MarkProcessed(seq1)
	tracker.MarkProcessed(seq2)
	tracker.MarkFailed(seq3) // Gap at seq3

	offsets := tracker.GetCommittableOffsets()

	// Gap at seq3 blocks seq2
	// So committable seq is 2, which includes:
	//   seq0 → (P0, 100)
	//   seq1 → (P1, 200)
	//   seq2 → (P0, 101)

	assert.Equal(t, int64(101), offsets[0], "P0 should commit up to 101")
	assert.Equal(t, int64(200), offsets[1], "P1 should commit up to 200 (blocked by seq3)")
}

func TestSequenceTracker_SequenceMonotonicity(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	tracker := burrow.NewSequenceTracker(logger)

	// Sequences should be monotonically increasing
	seq0 := tracker.AssignSequence(0, 100)
	seq1 := tracker.AssignSequence(0, 101)
	seq2 := tracker.AssignSequence(1, 200)
	seq3 := tracker.AssignSequence(1, 201)

	assert.Equal(t, int64(0), seq0)
	assert.Equal(t, int64(1), seq1)
	assert.Equal(t, int64(2), seq2)
	assert.Equal(t, int64(3), seq3)
}

func TestSequenceTracker_EdgeCases(t *testing.T) {
	t.Run("no sequences assigned", func(t *testing.T) {
		logger, _ := zap.NewDevelopment()
		tracker := burrow.NewSequenceTracker(logger)

		committable := tracker.GetCommittableSequence()
		assert.Equal(t, int64(-1), committable)

		offsets := tracker.GetCommittableOffsets()
		assert.Equal(t, 0, len(offsets))
	})

	t.Run("duplicate processing", func(t *testing.T) {
		logger, _ := zap.NewDevelopment()
		tracker := burrow.NewSequenceTracker(logger)

		// Process same sequence twice
		seq0 := tracker.AssignSequence(0, 100)
		tracker.RecordInflight(seq0)
		tracker.RecordInflight(seq0) // Duplicate
		tracker.MarkProcessed(seq0)
		tracker.MarkProcessed(seq0) // Duplicate

		seq1 := tracker.AssignSequence(0, 101)
		tracker.RecordInflight(seq1)
		tracker.MarkProcessed(seq1)

		committable := tracker.GetCommittableSequence()
		assert.Equal(t, int64(1), committable)
	})

	t.Run("out of order commit", func(t *testing.T) {
		logger, _ := zap.NewDevelopment()
		tracker := burrow.NewSequenceTracker(logger)

		// Process sequences
		for i := int64(0); i < 10; i++ {
			seq := tracker.AssignSequence(0, 100+i)
			tracker.RecordInflight(seq)
			tracker.MarkProcessed(seq)
		}

		// Commit sequence 5
		tracker.CommitSequence(5)

		// Should be able to get offsets for remaining
		offsets := tracker.GetCommittableOffsets()
		assert.Equal(t, int64(109), offsets[0], "Should commit up to offset 109")
	})
}
