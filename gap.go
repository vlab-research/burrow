// gap.go
package burrow

// FindCommittableOffset returns the highest contiguous offset that can be committed
// This is a pure function with no side effects - easily testable
//
// Parameters:
//   - processed: map of offsets that have been successfully processed
//   - lastCommitted: the last offset that was committed to Kafka
//   - highWatermark: the highest offset we've seen so far
//
// Returns: the highest offset where all offsets from lastCommitted+1 to that offset are processed
//
// Example:
//
//	processed = {0: true, 1: true, 2: true, 4: true}
//	lastCommitted = -1
//	highWatermark = 4
//	returns: 2 (can't commit 3 or 4 because 3 is missing - gap!)
func FindCommittableOffset(
	processed map[int64]bool,
	lastCommitted int64,
	highWatermark int64,
) int64 {
	committable := lastCommitted
	for offset := lastCommitted + 1; offset <= highWatermark; offset++ {
		if !processed[offset] {
			// Gap found! Can't commit beyond this point
			break
		}
		committable = offset
	}
	return committable
}

// HasGaps returns true if there are any gaps in the range [start, end]
// This is a pure function - easily testable
//
// Parameters:
//   - processed: map of offsets that have been successfully processed
//   - start: starting offset (inclusive)
//   - end: ending offset (inclusive)
//
// Returns: true if any offset in range is not processed
func HasGaps(processed map[int64]bool, start, end int64) bool {
	for offset := start; offset <= end; offset++ {
		if !processed[offset] {
			return true
		}
	}
	return false
}

// CountGaps returns the number of gaps in the range [start, end]
// This is a pure function - easily testable
//
// Parameters:
//   - processed: map of offsets that have been successfully processed
//   - start: starting offset (inclusive)
//   - end: ending offset (inclusive)
//
// Returns: count of offsets in range that are not processed
func CountGaps(processed map[int64]bool, start, end int64) int {
	count := 0
	for offset := start; offset <= end; offset++ {
		if !processed[offset] {
			count++
		}
	}
	return count
}

// GetGapRanges returns a list of gap ranges in the format [[start1, end1], [start2, end2], ...]
// This is a pure function - easily testable
//
// Parameters:
//   - processed: map of offsets that have been successfully processed
//   - start: starting offset (inclusive)
//   - end: ending offset (inclusive)
//
// Returns: slice of [start, end] pairs representing contiguous gap ranges
func GetGapRanges(processed map[int64]bool, start, end int64) [][2]int64 {
	var gaps [][2]int64
	inGap := false
	var gapStart int64

	for offset := start; offset <= end; offset++ {
		if !processed[offset] {
			if !inGap {
				// Start of new gap
				gapStart = offset
				inGap = true
			}
		} else {
			if inGap {
				// End of gap
				gaps = append(gaps, [2]int64{gapStart, offset - 1})
				inGap = false
			}
		}
	}

	// Close any remaining gap
	if inGap {
		gaps = append(gaps, [2]int64{gapStart, end})
	}

	return gaps
}
