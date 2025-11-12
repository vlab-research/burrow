// gap.go
package burrow

// FindCommittableOffset returns the highest contiguous offset that can be committed.
// This is a pure function with no side effects - easily testable.
func FindCommittableOffset(processed map[int64]bool, lastCommitted, highWatermark int64) int64 {
	committable := lastCommitted
	for offset := lastCommitted + 1; offset <= highWatermark; offset++ {
		if !processed[offset] {
			break // Gap found
		}
		committable = offset
	}
	return committable
}
