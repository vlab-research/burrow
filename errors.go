// errors.go
package burrow

import (
	"sync"
	"time"

	"go.uber.org/zap"
)

// ErrorTracker tracks errors and halts processing if threshold exceeded
// Inspired by Eddies' error tracking pattern
type ErrorTracker struct {
	mu                sync.Mutex
	consecutiveErrors int
	maxConsecutive    int
	totalErrors       int64
	logger            *zap.Logger
}

// NewErrorTracker creates a new error tracker
func NewErrorTracker(maxConsecutive int, logger *zap.Logger) *ErrorTracker {
	return &ErrorTracker{
		maxConsecutive: maxConsecutive,
		logger:         logger,
	}
}

// RecordError records an error and returns true if should halt
func (et *ErrorTracker) RecordError(partition int32, offset int64, err error) bool {
	et.mu.Lock()
	defer et.mu.Unlock()

	et.consecutiveErrors++
	et.totalErrors++

	shouldHalt := et.consecutiveErrors >= et.maxConsecutive

	if shouldHalt {
		et.logger.Error("error threshold exceeded, halting",
			zap.Int("consecutive_errors", et.consecutiveErrors),
			zap.Int("max_consecutive", et.maxConsecutive),
			zap.Int64("total_errors", et.totalErrors),
			zap.Int32("partition", partition),
			zap.Int64("offset", offset),
			zap.Error(err))
	} else {
		et.logger.Warn("processing error",
			zap.Int("consecutive_errors", et.consecutiveErrors),
			zap.Int32("partition", partition),
			zap.Int64("offset", offset),
			zap.Error(err))
	}

	return shouldHalt
}

// RecordSuccess resets consecutive error counter
func (et *ErrorTracker) RecordSuccess() {
	et.mu.Lock()
	defer et.mu.Unlock()

	if et.consecutiveErrors > 0 {
		et.logger.Debug("resetting consecutive error counter",
			zap.Int("was", et.consecutiveErrors))
		et.consecutiveErrors = 0
	}
}

// GetStats returns error statistics
func (et *ErrorTracker) GetStats() (consecutive int, total int64) {
	et.mu.Lock()
	defer et.mu.Unlock()
	return et.consecutiveErrors, et.totalErrors
}

// isRetriable determines if an error should be retried
func isRetriable(err error) bool {
	if err == nil {
		return false
	}

	// TODO: Add specific error type checking
	// For now, retry most errors
	return true
}

// calculateBackoff computes exponential backoff duration
func calculateBackoff(attempt int, base time.Duration) time.Duration {
	// Exponential: base * 2^attempt, capped at 60 seconds
	backoff := base * time.Duration(1<<uint(attempt))
	if backoff > 60*time.Second {
		backoff = 60 * time.Second
	}
	return backoff
}
