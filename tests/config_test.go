package burrow_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/vlab-research/fly/burrow"
	"go.uber.org/zap"
)

// TestDefaultConfig verifies default configuration values
func TestDefaultConfig(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	config := burrow.DefaultConfig(logger)

	// Verify all defaults
	assert.Equal(t, 10, config.NumWorkers, "Default NumWorkers should be 10")
	assert.Equal(t, 1000, config.JobQueueSize, "Default JobQueueSize should be 1000")
	assert.Equal(t, 1000, config.ResultQueueSize, "Default ResultQueueSize should be 1000")
	assert.Equal(t, 5*time.Second, config.CommitInterval, "Default CommitInterval should be 5s")
	assert.Equal(t, 1000, config.CommitBatchSize, "Default CommitBatchSize should be 1000")
	assert.Equal(t, burrow.FatalOnError, config.OnError, "Default OnError should be FatalOnError")
	assert.NotNil(t, config.Logger, "Logger should not be nil")
	assert.False(t, config.EnableMetrics, "EnableMetrics should default to false")
	assert.Equal(t, 30*time.Second, config.ShutdownTimeout, "Default ShutdownTimeout should be 30s")
}

// TestConfig_Validate_ValidConfig tests validation with valid config
func TestConfig_Validate_ValidConfig(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	config := burrow.DefaultConfig(logger)

	err := config.Validate()
	assert.NoError(t, err, "Valid config should pass validation")
}

// TestConfig_Validate_InvalidNumWorkers tests validation with invalid NumWorkers
func TestConfig_Validate_InvalidNumWorkers(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	tests := []struct {
		name       string
		numWorkers int
		expectErr  bool
	}{
		{"zero workers", 0, true},
		{"negative workers", -1, true},
		{"negative large", -100, true},
		{"one worker", 1, false},
		{"many workers", 1000, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := burrow.DefaultConfig(logger)
			config.NumWorkers = tt.numWorkers

			err := config.Validate()
			if tt.expectErr {
				assert.Error(t, err, "Should error with NumWorkers=%d", tt.numWorkers)
				assert.Contains(t, err.Error(), "NumWorkers", "Error should mention NumWorkers")
			} else {
				assert.NoError(t, err, "Should not error with NumWorkers=%d", tt.numWorkers)
			}
		})
	}
}

// TestConfig_Validate_NilLogger tests validation with nil logger
func TestConfig_Validate_NilLogger(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	config := burrow.DefaultConfig(logger)
	config.Logger = nil

	err := config.Validate()
	assert.Error(t, err, "Should error with nil logger")
	assert.Contains(t, err.Error(), "Logger", "Error should mention Logger")
}

// TestConfig_Validate_InvalidCommitInterval tests validation with invalid commit interval
func TestConfig_Validate_InvalidCommitInterval(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	tests := []struct {
		name           string
		commitInterval time.Duration
		expectErr      bool
	}{
		{"zero interval", 0, true},
		{"negative interval", -1 * time.Second, true},
		{"very small positive", 1 * time.Nanosecond, false},
		{"normal interval", 5 * time.Second, false},
		{"large interval", 1 * time.Hour, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := burrow.DefaultConfig(logger)
			config.CommitInterval = tt.commitInterval

			err := config.Validate()
			if tt.expectErr {
				assert.Error(t, err, "Should error with CommitInterval=%v", tt.commitInterval)
				assert.Contains(t, err.Error(), "CommitInterval", "Error should mention CommitInterval")
			} else {
				assert.NoError(t, err, "Should not error with CommitInterval=%v", tt.commitInterval)
			}
		})
	}
}

// TestConfig_Validate_AllInvalid tests config with multiple invalid fields
func TestConfig_Validate_AllInvalid(t *testing.T) {
	config := burrow.Config{
		NumWorkers:     0,  // Invalid
		Logger:         nil, // Invalid
		CommitInterval: 0,  // Invalid
	}

	err := config.Validate()
	assert.Error(t, err, "Should error with multiple invalid fields")
	// Should error on first invalid field it checks
}

// TestConfig_CustomValues tests config with custom values
func TestConfig_CustomValues(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	config := burrow.Config{
		NumWorkers:      100,
		JobQueueSize:    5000,
		ResultQueueSize: 5000,
		CommitInterval:  10 * time.Second,
		CommitBatchSize: 500,
		OnError:         burrow.FreezeOnError,
		Logger:          logger,
		EnableMetrics:   true,
		ShutdownTimeout: 60 * time.Second,
	}

	// Should validate successfully
	err := config.Validate()
	assert.NoError(t, err, "Custom valid config should pass validation")

	// Verify values are set
	assert.Equal(t, 100, config.NumWorkers)
	assert.Equal(t, 5000, config.JobQueueSize)
	assert.Equal(t, 10*time.Second, config.CommitInterval)
	assert.Equal(t, burrow.FreezeOnError, config.OnError)
	assert.True(t, config.EnableMetrics)
}

// TestConfig_MinimalConfig tests minimal valid configuration
func TestConfig_MinimalConfig(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	config := burrow.Config{
		NumWorkers:     1,
		Logger:         logger,
		CommitInterval: 1 * time.Nanosecond,
		// Other fields can be zero/default
	}

	err := config.Validate()
	assert.NoError(t, err, "Minimal valid config should pass")
}

// TestConfig_EdgeCaseValues tests edge case configuration values
func TestConfig_EdgeCaseValues(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	t.Run("very large values", func(t *testing.T) {
		config := burrow.Config{
			NumWorkers:      10000,
			JobQueueSize:    1000000,
			ResultQueueSize: 1000000,
			CommitInterval:  24 * time.Hour,
			CommitBatchSize: 1000000,
			OnError:         burrow.FreezeOnError,
			Logger:          logger,
			ShutdownTimeout: 24 * time.Hour,
		}

		err := config.Validate()
		assert.NoError(t, err, "Very large values should be valid")
	})

	t.Run("very small positive values", func(t *testing.T) {
		config := burrow.Config{
			NumWorkers:      1,
			JobQueueSize:    1,
			ResultQueueSize: 1,
			CommitInterval:  1 * time.Nanosecond,
			CommitBatchSize: 1,
			OnError:         burrow.FatalOnError,
			Logger:          logger,
			ShutdownTimeout: 1 * time.Nanosecond,
		}

		err := config.Validate()
		assert.NoError(t, err, "Very small positive values should be valid")
	})
}

// TestConfig_ZeroValues tests zero values where allowed
func TestConfig_ZeroValues(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	config := burrow.Config{
		NumWorkers:      1,
		Logger:          logger,
		CommitInterval:  1 * time.Second,
		JobQueueSize:    0, // Can be zero
		ResultQueueSize: 0, // Can be zero
		CommitBatchSize: 0, // Can be zero
		OnError:         burrow.FatalOnError,
		ShutdownTimeout: 0, // Can be zero
		EnableMetrics:   false,
	}

	err := config.Validate()
	assert.NoError(t, err, "Zero values for optional fields should be valid")
}

// TestConfig_ValidationErrorMessages tests error message quality
func TestConfig_ValidationErrorMessages(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	t.Run("invalid NumWorkers message", func(t *testing.T) {
		config := burrow.DefaultConfig(logger)
		config.NumWorkers = -5

		err := config.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "NumWorkers")
		assert.Contains(t, err.Error(), "-5")
	})

	t.Run("nil logger message", func(t *testing.T) {
		config := burrow.DefaultConfig(logger)
		config.Logger = nil

		err := config.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "Logger")
		assert.Contains(t, err.Error(), "required")
	})

	t.Run("invalid CommitInterval message", func(t *testing.T) {
		config := burrow.DefaultConfig(logger)
		config.CommitInterval = -10 * time.Second

		err := config.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "CommitInterval")
	})
}

// TestConfig_Immutability tests that validation doesn't modify config
func TestConfig_Immutability(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	config := burrow.DefaultConfig(logger)

	// Save original values
	originalWorkers := config.NumWorkers
	originalInterval := config.CommitInterval

	// Validate
	config.Validate()

	// Verify nothing changed
	assert.Equal(t, originalWorkers, config.NumWorkers)
	assert.Equal(t, originalInterval, config.CommitInterval)
}

// TestConfig_DefaultIsValid tests that DefaultConfig produces valid config
func TestConfig_DefaultIsValid(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	config := burrow.DefaultConfig(logger)

	err := config.Validate()
	assert.NoError(t, err, "DefaultConfig should always be valid")
}

// TestConfig_MultipleValidations tests validating same config multiple times
func TestConfig_MultipleValidations(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	config := burrow.DefaultConfig(logger)

	// Validate multiple times
	for i := 0; i < 5; i++ {
		err := config.Validate()
		assert.NoError(t, err, "Should validate successfully on attempt %d", i+1)
	}
}

// TestConfig_PartialDefaultOverride tests overriding some default values
func TestConfig_PartialDefaultOverride(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	config := burrow.DefaultConfig(logger)

	// Override only some values
	config.NumWorkers = 50
	config.CommitInterval = 10 * time.Second

	err := config.Validate()
	assert.NoError(t, err)

	// Verify overridden values
	assert.Equal(t, 50, config.NumWorkers)
	assert.Equal(t, 10*time.Second, config.CommitInterval)

	// Verify non-overridden defaults remain
	assert.Equal(t, 1000, config.JobQueueSize)
	assert.Equal(t, burrow.FatalOnError, config.OnError)
}

// TestConfig_ReasonableProductionValues tests production-ready configuration
func TestConfig_ReasonableProductionValues(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := burrow.Config{
		NumWorkers:      100,
		JobQueueSize:    10000,
		ResultQueueSize: 10000,
		CommitInterval:  5 * time.Second,
		CommitBatchSize: 5000,
		OnError:         burrow.FatalOnError,
		Logger:          logger,
		EnableMetrics:   true,
		ShutdownTimeout: 30 * time.Second,
	}

	err := config.Validate()
	assert.NoError(t, err, "Production config should be valid")
}

// TestConfig_HighThroughputValues tests config for high throughput scenarios
func TestConfig_HighThroughputValues(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := burrow.Config{
		NumWorkers:      500,
		JobQueueSize:    50000,
		ResultQueueSize: 50000,
		CommitInterval:  1 * time.Second,
		CommitBatchSize: 10000,
		OnError:         burrow.FatalOnError,
		Logger:          logger,
		EnableMetrics:   true,
		ShutdownTimeout: 60 * time.Second,
	}

	err := config.Validate()
	assert.NoError(t, err, "High throughput config should be valid")
}
