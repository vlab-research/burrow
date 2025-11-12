package burrow_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/confluentinc/confluent-kafka-go/v2/kafka"
	"github.com/stretchr/testify/assert"
	"github.com/vlab-research/fly/burrow"
	"go.uber.org/zap"
)

// TestWorkerPool_BasicJobProcessing tests basic worker pool functionality
func TestWorkerPool_BasicJobProcessing(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	wp := burrow.NewWorkerPool(5, 100, 100, logger)

	// Start workers
	wp.Start()
	defer wp.Stop()

	ctx := context.Background()
	var processed int32

	// Create a simple process function
	processFunc := func(ctx context.Context, msg *kafka.Message) error {
		atomic.AddInt32(&processed, 1)
		return nil
	}

	// Submit 10 jobs
	numJobs := 10
	for i := 0; i < numJobs; i++ {
		job := &burrow.Job{
			Sequence:    int64(i),
			Partition:   0,
			Offset:      int64(i),
			Message:     &kafka.Message{Value: []byte("test")},
			ProcessFunc: processFunc,
		}
		err := wp.SubmitJob(ctx, job)
		assert.NoError(t, err)
	}

	// Collect results
	for i := 0; i < numJobs; i++ {
		result := <-wp.Results()
		assert.True(t, result.Success, "Expected successful result")
		assert.Equal(t, int32(0), result.Partition)
		assert.Nil(t, result.Error)
	}

	// Verify all jobs were processed
	assert.Equal(t, int32(numJobs), atomic.LoadInt32(&processed))
}

// TestWorkerPool_ConcurrentProcessing verifies parallel job execution
func TestWorkerPool_ConcurrentProcessing(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	numWorkers := 10
	wp := burrow.NewWorkerPool(numWorkers, 100, 100, logger)

	wp.Start()
	defer wp.Stop()

	ctx := context.Background()
	var processing int32
	var maxConcurrent int32

	// Process function that tracks concurrent execution
	processFunc := func(ctx context.Context, msg *kafka.Message) error {
		current := atomic.AddInt32(&processing, 1)

		// Update max concurrent if needed
		for {
			max := atomic.LoadInt32(&maxConcurrent)
			if current <= max || atomic.CompareAndSwapInt32(&maxConcurrent, max, current) {
				break
			}
		}

		// Simulate work
		time.Sleep(50 * time.Millisecond)

		atomic.AddInt32(&processing, -1)
		return nil
	}

	// Submit enough jobs to saturate workers
	numJobs := 50
	for i := 0; i < numJobs; i++ {
		job := &burrow.Job{
			Sequence:    int64(i),
			Partition:   0,
			Offset:      int64(i),
			Message:     &kafka.Message{Value: []byte("test")},
			ProcessFunc: processFunc,
		}
		err := wp.SubmitJob(ctx, job)
		assert.NoError(t, err)
	}

	// Collect results
	for i := 0; i < numJobs; i++ {
		<-wp.Results()
	}

	// Verify we achieved parallel execution (at least 5 concurrent)
	max := atomic.LoadInt32(&maxConcurrent)
	assert.GreaterOrEqual(t, max, int32(5), "Expected at least 5 concurrent executions")
	t.Logf("Max concurrent executions: %d", max)
}

// TestWorkerPool_ErrorHandling tests error propagation
func TestWorkerPool_ErrorHandling(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	wp := burrow.NewWorkerPool(5, 100, 100, logger)

	wp.Start()
	defer wp.Stop()

	ctx := context.Background()

	// Process function that fails
	processFunc := func(ctx context.Context, msg *kafka.Message) error {
		return assert.AnError
	}

	// Submit failing job
	job := &burrow.Job{
		Sequence:    0,
		Partition:   0,
		Offset:      0,
		Message:     &kafka.Message{Value: []byte("test")},
		ProcessFunc: processFunc,
	}
	err := wp.SubmitJob(ctx, job)
	assert.NoError(t, err)

	// Get result
	result := <-wp.Results()
	assert.False(t, result.Success)
	assert.Error(t, result.Error)
	assert.Equal(t, assert.AnError, result.Error)
}

// TestWorkerPool_ContextCancellation tests graceful shutdown
func TestWorkerPool_ContextCancellation(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	wp := burrow.NewWorkerPool(5, 100, 100, logger)

	wp.Start()
	defer wp.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	var processed int32

	// Use a channel to block workers until we're ready
	startProcessing := make(chan struct{})

	// Process function with cancellation check
	processFunc := func(ctx context.Context, msg *kafka.Message) error {
		// Block until we signal to start processing
		<-startProcessing

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			atomic.AddInt32(&processed, 1)
			time.Sleep(10 * time.Millisecond)
			return nil
		}
	}

	// Submit jobs to fill the queue buffer (jobQueueSize = 100)
	numJobs := 100
	for i := 0; i < numJobs; i++ {
		job := &burrow.Job{
			Partition:   0,
			Offset:      int64(i),
			Message:     &kafka.Message{Value: []byte("test")},
			ProcessFunc: processFunc,
		}
		wp.SubmitJob(ctx, job)
	}

	// Now the queue is full (100 jobs) but workers are blocked
	// Cancel the context
	cancel()

	// Try to submit after cancellation - should fail because:
	// 1. Buffer is full (100 jobs queued, workers blocked)
	// 2. Context is cancelled
	// The send will block, so context check will win
	job := &burrow.Job{
		Partition:   0,
		Offset:      999,
		Message:     &kafka.Message{Value: []byte("test")},
		ProcessFunc: processFunc,
	}
	err := wp.SubmitJob(ctx, job)
	assert.Error(t, err, "Expected error when submitting to cancelled context")
	assert.Equal(t, context.Canceled, err)

	// Unblock workers so they can drain and shutdown cleanly
	close(startProcessing)
}

// TestWorkerPool_JobOrdering tests that results maintain job information
func TestWorkerPool_JobOrdering(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	wp := burrow.NewWorkerPool(3, 100, 100, logger)

	wp.Start()
	defer wp.Stop()

	ctx := context.Background()

	// Process function
	processFunc := func(ctx context.Context, msg *kafka.Message) error {
		// Add variable delay to ensure out-of-order processing
		delay := time.Duration(msg.Value[0]) * time.Millisecond
		time.Sleep(delay)
		return nil
	}

	// Submit jobs with different delays
	offsets := []int64{10, 5, 15, 2, 20}
	for _, offset := range offsets {
		job := &burrow.Job{
			Partition:   0,
			Offset:      offset,
			Message:     &kafka.Message{Value: []byte{byte(offset)}},
			ProcessFunc: processFunc,
		}
		err := wp.SubmitJob(ctx, job)
		assert.NoError(t, err)
	}

	// Collect results - they may arrive out of order
	results := make(map[int64]bool)
	for i := 0; i < len(offsets); i++ {
		result := <-wp.Results()
		results[result.Offset] = result.Success
	}

	// Verify all offsets were processed
	for _, offset := range offsets {
		assert.True(t, results[offset], "Expected offset %d to be processed", offset)
	}
}

// TestWorkerPool_Backpressure tests queue full behavior
func TestWorkerPool_Backpressure(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	// Create pool with very small queue
	smallQueueSize := 5
	wp := burrow.NewWorkerPool(2, smallQueueSize, 100, logger)

	wp.Start()
	defer wp.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Slow process function to fill the queue
	processFunc := func(ctx context.Context, msg *kafka.Message) error {
		time.Sleep(200 * time.Millisecond)
		return nil
	}

	// Submit jobs that will fill the queue
	var wg sync.WaitGroup
	submitted := make([]bool, 20)

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			job := &burrow.Job{
				Partition:   0,
				Offset:      int64(idx),
				Message:     &kafka.Message{Value: []byte("test")},
				ProcessFunc: processFunc,
			}
			err := wp.SubmitJob(ctx, job)
			if err == nil {
				submitted[idx] = true
			}
		}(i)
	}

	// Wait for submissions to complete
	wg.Wait()

	// Some submissions should have succeeded
	successCount := 0
	for _, success := range submitted {
		if success {
			successCount++
		}
	}

	t.Logf("Successfully submitted %d jobs", successCount)
	assert.Greater(t, successCount, 0, "Expected some jobs to be submitted")
}

// TestWorkerPool_MultiplePartitions tests handling jobs from different partitions
func TestWorkerPool_MultiplePartitions(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	wp := burrow.NewWorkerPool(5, 100, 100, logger)

	wp.Start()
	defer wp.Stop()

	ctx := context.Background()

	// Track processing by partition
	partitionCounts := make(map[int32]int32)
	var mu sync.Mutex

	processFunc := func(ctx context.Context, msg *kafka.Message) error {
		// Extract partition from message key
		partition := int32(msg.Key[0])
		mu.Lock()
		partitionCounts[partition]++
		mu.Unlock()
		return nil
	}

	// Submit jobs for 3 partitions
	numPartitions := 3
	jobsPerPartition := 10

	for p := 0; p < numPartitions; p++ {
		for i := 0; i < jobsPerPartition; i++ {
			job := &burrow.Job{
				Partition:   int32(p),
				Offset:      int64(i),
				Message:     &kafka.Message{Key: []byte{byte(p)}, Value: []byte("test")},
				ProcessFunc: processFunc,
			}
			err := wp.SubmitJob(ctx, job)
			assert.NoError(t, err)
		}
	}

	// Collect results
	totalJobs := numPartitions * jobsPerPartition
	for i := 0; i < totalJobs; i++ {
		result := <-wp.Results()
		assert.True(t, result.Success)
	}

	// Verify each partition processed correct number of jobs
	mu.Lock()
	defer mu.Unlock()
	for p := 0; p < numPartitions; p++ {
		count := partitionCounts[int32(p)]
		assert.Equal(t, int32(jobsPerPartition), count,
			"Partition %d processed %d jobs, expected %d", p, count, jobsPerPartition)
	}
}

// TestWorkerPool_GracefulStop tests clean shutdown
func TestWorkerPool_GracefulStop(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	wp := burrow.NewWorkerPool(5, 100, 100, logger)

	wp.Start()

	ctx := context.Background()
	var processed int32

	processFunc := func(ctx context.Context, msg *kafka.Message) error {
		atomic.AddInt32(&processed, 1)
		time.Sleep(10 * time.Millisecond)
		return nil
	}

	// Submit a few jobs
	numJobs := 5
	for i := 0; i < numJobs; i++ {
		job := &burrow.Job{
			Partition:   0,
			Offset:      int64(i),
			Message:     &kafka.Message{Value: []byte("test")},
			ProcessFunc: processFunc,
		}
		wp.SubmitJob(ctx, job)
	}

	// Wait a bit for processing to start
	time.Sleep(20 * time.Millisecond)

	// Stop the pool
	wp.Stop()

	// Verify some jobs were processed
	count := atomic.LoadInt32(&processed)
	assert.Greater(t, count, int32(0), "Expected at least some jobs to be processed before stop")
	t.Logf("Processed %d jobs before stop", count)
}

// TestWorkerPool_StressTest submits many jobs rapidly
func TestWorkerPool_StressTest(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	wp := burrow.NewWorkerPool(20, 1000, 1000, logger)

	wp.Start()
	defer wp.Stop()

	ctx := context.Background()
	var processed int32

	processFunc := func(ctx context.Context, msg *kafka.Message) error {
		atomic.AddInt32(&processed, 1)
		// Very fast processing
		time.Sleep(1 * time.Millisecond)
		return nil
	}

	// Submit many jobs
	numJobs := 1000
	start := time.Now()

	for i := 0; i < numJobs; i++ {
		job := &burrow.Job{
			Partition:   int32(i % 10), // Spread across 10 partitions
			Offset:      int64(i),
			Message:     &kafka.Message{Value: []byte("test")},
			ProcessFunc: processFunc,
		}
		err := wp.SubmitJob(ctx, job)
		assert.NoError(t, err)
	}

	// Collect all results
	successCount := 0
	for i := 0; i < numJobs; i++ {
		result := <-wp.Results()
		if result.Success {
			successCount++
		}
	}

	duration := time.Since(start)

	// Verify all succeeded
	assert.Equal(t, numJobs, successCount)
	assert.Equal(t, int32(numJobs), atomic.LoadInt32(&processed))

	t.Logf("Processed %d jobs in %v (%.2f jobs/sec)",
		numJobs, duration, float64(numJobs)/duration.Seconds())
}
