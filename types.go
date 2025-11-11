// types.go
package burrow

import (
	"context"
	"github.com/confluentinc/confluent-kafka-go/v2/kafka"
)

// ProcessFunc is the user-defined function for processing a message
type ProcessFunc func(context.Context, *kafka.Message) error

// MessageInfo tracks partition/offset for a sequence number
type MessageInfo struct {
	Partition int32
	Offset    int64
}

// Job represents a message to be processed by a worker
type Job struct {
	Sequence    int64 // Global sequence number for sequential tracking
	Partition   int32
	Offset      int64
	Message     *kafka.Message
	ProcessFunc ProcessFunc
	Attempt     int // Retry attempt number
}

// Result represents the outcome of processing a job
type Result struct {
	Sequence  int64 // Global sequence number for sequential tracking
	Partition int32
	Offset    int64
	Success   bool
	Error     error
	Attempt   int
	Job       *Job // Original job for retry
}

// Stats contains runtime statistics
type Stats struct {
	MessagesProcessed int64
	MessagesFailed    int64
	OffsetsCommitted  int64
	WorkersActive     int
	JobsQueued        int
}
