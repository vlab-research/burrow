package burrow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	burrow "github.com/vlab-research/fly/burrow"
)

// TestFindCommittableOffset tests the pure gap detection function
func TestFindCommittableOffset(t *testing.T) {
	tests := []struct {
		name          string
		processed     map[int64]bool
		lastCommitted int64
		highWatermark int64
		want          int64
	}{
		{
			name:          "no gaps - all contiguous",
			processed:     map[int64]bool{0: true, 1: true, 2: true},
			lastCommitted: -1,
			highWatermark: 2,
			want:          2,
		},
		{
			name:          "gap at offset 1",
			processed:     map[int64]bool{0: true, 2: true},
			lastCommitted: -1,
			highWatermark: 2,
			want:          0,
		},
		{
			name:          "gap at offset 3",
			processed:     map[int64]bool{0: true, 1: true, 2: true, 4: true},
			lastCommitted: -1,
			highWatermark: 4,
			want:          2,
		},
		{
			name:          "nothing processed",
			processed:     map[int64]bool{},
			lastCommitted: -1,
			highWatermark: 5,
			want:          -1,
		},
		{
			name:          "already committed up to 5",
			processed:     map[int64]bool{6: true, 7: true, 8: true},
			lastCommitted: 5,
			highWatermark: 8,
			want:          8,
		},
		{
			name:          "committed up to 5, gap at 7",
			processed:     map[int64]bool{6: true, 8: true},
			lastCommitted: 5,
			highWatermark: 8,
			want:          6,
		},
		{
			name:          "first offset has gap",
			processed:     map[int64]bool{1: true, 2: true},
			lastCommitted: -1,
			highWatermark: 2,
			want:          -1,
		},
		{
			name:          "single processed offset",
			processed:     map[int64]bool{0: true},
			lastCommitted: -1,
			highWatermark: 0,
			want:          0,
		},
		{
			name:          "multiple gaps",
			processed:     map[int64]bool{0: true, 2: true, 4: true, 6: true},
			lastCommitted: -1,
			highWatermark: 6,
			want:          0,
		},
		{
			name:          "empty processed map",
			processed:     map[int64]bool{},
			lastCommitted: -1,
			highWatermark: -1,
			want:          -1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := burrow.FindCommittableOffset(tt.processed, tt.lastCommitted, tt.highWatermark)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFindCommittableOffset_RealWorldScenario(t *testing.T) {
	// Simulate messages completing out of order
	processed := make(map[int64]bool)
	lastCommitted := int64(-1)

	// Process messages: 0, 2, 4, 1, 3
	// After each, check committable offset

	// After 0 completes
	processed[0] = true
	committable := burrow.FindCommittableOffset(processed, lastCommitted, 0)
	assert.Equal(t, int64(0), committable, "After 0: should be able to commit 0")

	// After 2 completes (gap at 1)
	processed[2] = true
	committable = burrow.FindCommittableOffset(processed, lastCommitted, 2)
	assert.Equal(t, int64(0), committable, "After 2: still gap at 1, can only commit 0")

	// After 4 completes (still gaps at 1 and 3)
	processed[4] = true
	committable = burrow.FindCommittableOffset(processed, lastCommitted, 4)
	assert.Equal(t, int64(0), committable, "After 4: still gap at 1, can only commit 0")

	// After 1 completes (gap at 3)
	processed[1] = true
	committable = burrow.FindCommittableOffset(processed, lastCommitted, 4)
	assert.Equal(t, int64(2), committable, "After 1: can commit 0,1,2 (gap at 3)")

	// After 3 completes (no gaps)
	processed[3] = true
	committable = burrow.FindCommittableOffset(processed, lastCommitted, 4)
	assert.Equal(t, int64(4), committable, "After 3: can commit all (0-4)")
}

// TestFindCommittableOffset_AfterCommit tests behavior after committing
func TestFindCommittableOffset_AfterCommit(t *testing.T) {
	processed := map[int64]bool{
		0: true, 1: true, 2: true,
		// 3 is missing (gap)
		4: true, 5: true,
	}

	// Before commit
	committable := burrow.FindCommittableOffset(processed, -1, 5)
	assert.Equal(t, int64(2), committable, "Can only commit up to 2 (gap at 3)")

	// After committing 2, we can clean up 0,1,2 from processed map
	// In real code, CommitOffset would do this cleanup
	lastCommitted := int64(2)
	delete(processed, 0)
	delete(processed, 1)
	delete(processed, 2)

	// Now check what we can commit
	committable = burrow.FindCommittableOffset(processed, lastCommitted, 5)
	assert.Equal(t, int64(2), committable, "Still can't commit beyond 2 (gap at 3)")

	// If 3 arrives
	processed[3] = true
	committable = burrow.FindCommittableOffset(processed, lastCommitted, 5)
	assert.Equal(t, int64(5), committable, "Now can commit up to 5")
}
