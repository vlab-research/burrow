// config.go
package burrow

import (
	"fmt"
	"time"

	"go.uber.org/zap"
)

// OnErrorBehavior defines what happens when message processing returns an error
type OnErrorBehavior int

const (
	// FreezeOnError stops polling new messages, waits for inflight to complete,
	// commits up to the failed message, then blocks forever.
	// Requires external restart (K8s, systemd, etc.)
	// Use this when you want to investigate before restarting.
	FreezeOnError OnErrorBehavior = iota

	// FatalOnError immediately exits the process with os.Exit(1) on first error.
	// No graceful shutdown, no waiting for inflight.
	// Use this for fast failure and immediate restart (with crash loop backoff).
	FatalOnError
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
	OnError OnErrorBehavior // Behavior when processFunc returns error (default: FreezeOnError)

	// Logging and metrics
	Logger        *zap.Logger // Logger instance (required)
	EnableMetrics bool        // Enable Prometheus metrics (default: false)

	// Advanced options
	ShutdownTimeout time.Duration // Graceful shutdown timeout (default: 30s)
}

// DefaultConfig returns a config with sensible defaults
func DefaultConfig(logger *zap.Logger) Config {
	return Config{
		NumWorkers:      10,
		JobQueueSize:    1000,
		ResultQueueSize: 1000,
		CommitInterval:  5 * time.Second,
		CommitBatchSize: 1000,
		OnError:         FatalOnError, // Blow up immediately on error (fast failure)
		Logger:          logger,
		EnableMetrics:   false,
		ShutdownTimeout: 30 * time.Second,
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
