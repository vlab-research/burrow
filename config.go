// config.go
package burrow

import (
	"fmt"
	"time"

	"go.uber.org/zap"
)

// Config contains configuration for the Pool
type Config struct {
	// Worker pool configuration
	NumWorkers      int           // Number of concurrent workers (default: 10)
	JobQueueSize    int           // Size of job queue buffer (default: 1000)
	ResultQueueSize int           // Size of result queue buffer (default: 1000)

	// Commit configuration
	CommitInterval  time.Duration // How often to commit offsets (default: 5s)
	CommitBatchSize int           // Max messages before forcing commit (default: 1000)

	// Error handling
	MaxConsecutiveErrors int           // Max consecutive errors before halt (default: 10)
	MaxRetries           int           // Max retries per message (default: 3)
	RetryBackoffBase     time.Duration // Base backoff duration (default: 100ms)

	// Logging and metrics
	Logger        *zap.Logger // Logger instance (required)
	EnableMetrics bool        // Enable Prometheus metrics (default: false)

	// Advanced options
	EnableOrderedProcessing bool          // Ensure per-partition ordering (default: false)
	ShutdownTimeout         time.Duration // Graceful shutdown timeout (default: 30s)
}

// DefaultConfig returns a config with sensible defaults
func DefaultConfig(logger *zap.Logger) Config {
	return Config{
		NumWorkers:              10,
		JobQueueSize:            1000,
		ResultQueueSize:         1000,
		CommitInterval:          5 * time.Second,
		CommitBatchSize:         1000,
		MaxConsecutiveErrors:    10,
		MaxRetries:              3,
		RetryBackoffBase:        100 * time.Millisecond,
		Logger:                  logger,
		EnableMetrics:           false,
		EnableOrderedProcessing: false,
		ShutdownTimeout:         30 * time.Second,
	}
}

// Validate checks if config is valid
func (c Config) Validate() error {
	if c.NumWorkers <= 0 {
		return fmt.Errorf("NumWorkers must be > 0, got %d", c.NumWorkers)
	}
	if c.Logger == nil {
		return fmt.Errorf("Logger is required")
	}
	if c.CommitInterval <= 0 {
		return fmt.Errorf("CommitInterval must be > 0")
	}
	return nil
}
