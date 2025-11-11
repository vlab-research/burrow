package burrow_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/vlab-research/fly/burrow"
	"go.uber.org/zap"
)

// TestErrorTracker_BasicErrorTracking tests basic error recording
func TestErrorTracker_BasicErrorTracking(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	maxConsecutive := 5
	et := burrow.NewErrorTracker(maxConsecutive, logger)

	testErr := errors.New("test error")

	// Record errors below threshold
	for i := 0; i < maxConsecutive-1; i++ {
		shouldHalt := et.RecordError(0, int64(i), testErr)
		assert.False(t, shouldHalt, "Should not halt before threshold")
	}

	// Verify stats
	consecutive, total := et.GetStats()
	assert.Equal(t, maxConsecutive-1, consecutive)
	assert.Equal(t, int64(maxConsecutive-1), total)
}

// TestErrorTracker_ThresholdExceeded tests halting when threshold is reached
func TestErrorTracker_ThresholdExceeded(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	maxConsecutive := 3
	et := burrow.NewErrorTracker(maxConsecutive, logger)

	testErr := errors.New("test error")

	// Record errors up to threshold
	for i := 0; i < maxConsecutive-1; i++ {
		shouldHalt := et.RecordError(0, int64(i), testErr)
		assert.False(t, shouldHalt, "Should not halt yet")
	}

	// This error should trigger halt
	shouldHalt := et.RecordError(0, int64(maxConsecutive), testErr)
	assert.True(t, shouldHalt, "Should halt when threshold reached")

	// Verify stats
	consecutive, total := et.GetStats()
	assert.Equal(t, maxConsecutive, consecutive)
	assert.Equal(t, int64(maxConsecutive), total)
}

// TestErrorTracker_SuccessResetsConsecutive tests that success resets counter
func TestErrorTracker_SuccessResetsConsecutive(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	maxConsecutive := 5
	et := burrow.NewErrorTracker(maxConsecutive, logger)

	testErr := errors.New("test error")

	// Record some errors
	for i := 0; i < 3; i++ {
		shouldHalt := et.RecordError(0, int64(i), testErr)
		assert.False(t, shouldHalt)
	}

	// Record success - should reset consecutive counter
	et.RecordSuccess()

	consecutive, total := et.GetStats()
	assert.Equal(t, 0, consecutive, "Consecutive errors should be reset to 0")
	assert.Equal(t, int64(3), total, "Total errors should remain unchanged")

	// Can now record more errors before hitting threshold
	for i := 0; i < maxConsecutive-1; i++ {
		shouldHalt := et.RecordError(0, int64(i+10), testErr)
		assert.False(t, shouldHalt, "Should not halt after reset")
	}

	consecutive, total = et.GetStats()
	assert.Equal(t, maxConsecutive-1, consecutive)
	assert.Equal(t, int64(3+maxConsecutive-1), total)
}

// TestErrorTracker_MultipleSuccesses tests repeated success calls
func TestErrorTracker_MultipleSuccesses(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	maxConsecutive := 5
	et := burrow.NewErrorTracker(maxConsecutive, logger)

	testErr := errors.New("test error")

	// Record errors
	et.RecordError(0, 0, testErr)
	et.RecordError(0, 1, testErr)

	// Multiple successes should keep consecutive at 0
	et.RecordSuccess()
	et.RecordSuccess()
	et.RecordSuccess()

	consecutive, total := et.GetStats()
	assert.Equal(t, 0, consecutive)
	assert.Equal(t, int64(2), total)
}

// TestErrorTracker_DifferentPartitions tests errors from multiple partitions
func TestErrorTracker_DifferentPartitions(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	maxConsecutive := 3
	et := burrow.NewErrorTracker(maxConsecutive, logger)

	testErr := errors.New("test error")

	// Record errors from different partitions
	// (ErrorTracker treats all errors the same, regardless of partition)
	et.RecordError(0, 0, testErr)
	et.RecordError(1, 0, testErr)
	et.RecordError(2, 0, testErr)

	// Should halt because we hit threshold (3 consecutive errors across any partition)
	consecutive, _ := et.GetStats()
	assert.Equal(t, 3, consecutive)
}

// TestErrorTracker_ErrorPatterns tests alternating error/success patterns
func TestErrorTracker_ErrorPatterns(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	maxConsecutive := 5
	testErr := errors.New("test error")

	tests := []struct {
		name              string
		actions           []bool // true = error, false = success
		expectedHalt      bool
		expectedConsec    int
		expectedTotal     int64
	}{
		{
			name:           "alternating errors and successes",
			actions:        []bool{true, false, true, false, true, false},
			expectedHalt:   false,
			expectedConsec: 0,
			expectedTotal:  3,
		},
		{
			name:           "two errors then success",
			actions:        []bool{true, true, false, true, true, false},
			expectedHalt:   false,
			expectedConsec: 0,
			expectedTotal:  4,
		},
		{
			name:           "ramp up to threshold",
			actions:        []bool{true, true, false, true, true, true, true, true},
			expectedHalt:   true,
			expectedConsec: 5,
			expectedTotal:  7,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			et := burrow.NewErrorTracker(maxConsecutive, logger)
			halted := false

			for i, isError := range tt.actions {
				if isError {
					shouldHalt := et.RecordError(0, int64(i), testErr)
					if shouldHalt {
						halted = true
					}
				} else {
					et.RecordSuccess()
				}
			}

			assert.Equal(t, tt.expectedHalt, halted, "Halt condition mismatch")
			consecutive, total := et.GetStats()
			assert.Equal(t, tt.expectedConsec, consecutive, "Consecutive count mismatch")
			assert.Equal(t, tt.expectedTotal, total, "Total count mismatch")
		})
	}
}

// TestErrorTracker_ZeroThreshold tests edge case with threshold of 0
func TestErrorTracker_ZeroThreshold(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	maxConsecutive := 0
	et := burrow.NewErrorTracker(maxConsecutive, logger)

	testErr := errors.New("test error")

	// First error should trigger halt immediately
	shouldHalt := et.RecordError(0, 0, testErr)
	assert.True(t, shouldHalt, "Should halt immediately with threshold 0")
}

// TestErrorTracker_OneThreshold tests threshold of 1
func TestErrorTracker_OneThreshold(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	maxConsecutive := 1
	et := burrow.NewErrorTracker(maxConsecutive, logger)

	testErr := errors.New("test error")

	// First error should trigger halt
	shouldHalt := et.RecordError(0, 0, testErr)
	assert.True(t, shouldHalt, "Should halt on first error with threshold 1")
}

// TestErrorTracker_HighThreshold tests larger threshold values
func TestErrorTracker_HighThreshold(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	maxConsecutive := 100
	et := burrow.NewErrorTracker(maxConsecutive, logger)

	testErr := errors.New("test error")

	// Record 99 errors
	for i := 0; i < maxConsecutive-1; i++ {
		shouldHalt := et.RecordError(0, int64(i), testErr)
		assert.False(t, shouldHalt, "Should not halt before threshold")
	}

	// 100th error should trigger halt
	shouldHalt := et.RecordError(0, int64(maxConsecutive), testErr)
	assert.True(t, shouldHalt, "Should halt on 100th error")

	consecutive, total := et.GetStats()
	assert.Equal(t, maxConsecutive, consecutive)
	assert.Equal(t, int64(maxConsecutive), total)
}

// TestErrorTracker_StatsAccuracy tests accurate stat tracking
func TestErrorTracker_StatsAccuracy(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	maxConsecutive := 10
	et := burrow.NewErrorTracker(maxConsecutive, logger)

	testErr := errors.New("test error")

	// Track expected values
	expectedTotal := int64(0)
	expectedConsec := 0

	// Record 5 errors
	for i := 0; i < 5; i++ {
		et.RecordError(0, int64(i), testErr)
		expectedTotal++
		expectedConsec++
	}

	consecutive, total := et.GetStats()
	assert.Equal(t, expectedConsec, consecutive)
	assert.Equal(t, expectedTotal, total)

	// Success resets consecutive
	et.RecordSuccess()
	expectedConsec = 0

	consecutive, total = et.GetStats()
	assert.Equal(t, expectedConsec, consecutive)
	assert.Equal(t, expectedTotal, total)

	// Record 3 more errors
	for i := 0; i < 3; i++ {
		et.RecordError(0, int64(i+10), testErr)
		expectedTotal++
		expectedConsec++
	}

	consecutive, total = et.GetStats()
	assert.Equal(t, expectedConsec, consecutive)
	assert.Equal(t, expectedTotal, total)
}

// TestErrorTracker_ConcurrentAccess tests thread-safe access
func TestErrorTracker_ConcurrentAccess(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	maxConsecutive := 100
	et := burrow.NewErrorTracker(maxConsecutive, logger)

	testErr := errors.New("test error")

	// Concurrently record errors and successes
	done := make(chan bool)
	numGoroutines := 10
	actionsPerGoroutine := 50

	for g := 0; g < numGoroutines; g++ {
		go func(id int) {
			for i := 0; i < actionsPerGoroutine; i++ {
				if i%2 == 0 {
					et.RecordError(int32(id), int64(i), testErr)
				} else {
					et.RecordSuccess()
				}
			}
			done <- true
		}(g)
	}

	// Wait for all goroutines
	for g := 0; g < numGoroutines; g++ {
		<-done
	}

	// Just verify we can read stats without panic
	consecutive, total := et.GetStats()
	assert.GreaterOrEqual(t, total, int64(0))
	assert.GreaterOrEqual(t, consecutive, 0)
	t.Logf("Concurrent test: consecutive=%d, total=%d", consecutive, total)
}

// TestErrorTracker_DifferentErrors tests tracking different error types
func TestErrorTracker_DifferentErrors(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	maxConsecutive := 5
	et := burrow.NewErrorTracker(maxConsecutive, logger)

	// Different error types
	err1 := errors.New("database error")
	err2 := errors.New("network error")
	err3 := errors.New("timeout error")

	// All errors count toward consecutive threshold
	et.RecordError(0, 0, err1)
	et.RecordError(0, 1, err2)
	et.RecordError(0, 2, err3)
	et.RecordError(0, 3, err1)
	et.RecordError(0, 4, err2)

	// Should halt after 5 consecutive errors of any type
	consecutive, total := et.GetStats()
	assert.Equal(t, 5, consecutive)
	assert.Equal(t, int64(5), total)
}

// TestErrorTracker_NoSuccessWhenZeroConsecutive tests success with no prior errors
func TestErrorTracker_NoSuccessWhenZeroConsecutive(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	maxConsecutive := 5
	et := burrow.NewErrorTracker(maxConsecutive, logger)

	// Call success when consecutive is already 0
	et.RecordSuccess()
	et.RecordSuccess()

	consecutive, total := et.GetStats()
	assert.Equal(t, 0, consecutive)
	assert.Equal(t, int64(0), total)
}

// TestErrorTracker_LongRunningTracking tests tracking over extended period
func TestErrorTracker_LongRunningTracking(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	maxConsecutive := 10
	et := burrow.NewErrorTracker(maxConsecutive, logger)

	testErr := errors.New("test error")

	// Simulate long-running processing with occasional errors
	totalExpected := int64(0)
	for cycle := 0; cycle < 10; cycle++ {
		// Process 100 messages with 5% error rate
		for i := 0; i < 100; i++ {
			if i%20 == 0 { // 5% error rate
				et.RecordError(0, int64(cycle*100+i), testErr)
				totalExpected++
			} else {
				et.RecordSuccess()
			}
		}
	}

	consecutive, total := et.GetStats()
	// Consecutive should be reset (not hit threshold)
	assert.Equal(t, 0, consecutive, "Consecutive should be 0 with successes between errors")
	assert.Equal(t, totalExpected, total)
}
