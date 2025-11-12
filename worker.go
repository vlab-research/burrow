// worker.go
package burrow

import (
	"context"
	"time"

	"go.uber.org/zap"
)

// Worker processes jobs from the jobs channel
type Worker struct {
	id          int
	jobsChan    <-chan *Job
	resultsChan chan<- *Result
	logger      *zap.Logger
}

// run starts the worker loop (runs in its own goroutine)
func (w *Worker) run(ctx context.Context) {
	w.logger.Info("worker started", zap.Int("worker_id", w.id))
	defer w.logger.Info("worker stopped", zap.Int("worker_id", w.id))

	for {
		select {
		case <-ctx.Done():
			return

		case job := <-w.jobsChan:
			if job == nil {
				// Channel closed, exit gracefully
				return
			}
			w.processJob(ctx, job)
		}
	}
}

// processJob executes the user's ProcessFunc and sends result
func (w *Worker) processJob(ctx context.Context, job *Job) {
	start := time.Now()

	// Call user's processing function
	err := job.ProcessFunc(ctx, job.Message)

	duration := time.Since(start)

	// Create result
	result := &Result{
		Sequence:  job.Sequence,
		Partition: job.Partition,
		Offset:    job.Offset,
		Success:   err == nil,
		Error:     err,
	}

	// Log result
	if err != nil {
		w.logger.Error("job failed",
			zap.Int("worker_id", w.id),
			zap.Int64("sequence", job.Sequence),
			zap.Int32("partition", job.Partition),
			zap.Int64("offset", job.Offset),
			zap.Duration("duration", duration),
			zap.Error(err))
	} else {
		w.logger.Debug("job succeeded",
			zap.Int("worker_id", w.id),
			zap.Int64("sequence", job.Sequence),
			zap.Int32("partition", job.Partition),
			zap.Int64("offset", job.Offset),
			zap.Duration("duration", duration))
	}

	// Send result (blocks if result channel is full - backpressure)
	select {
	case w.resultsChan <- result:
	case <-ctx.Done():
		return
	}
}

// WorkerPool manages a pool of workers
type WorkerPool struct {
	numWorkers  int
	jobsChan    chan *Job
	resultsChan chan *Result
	logger      *zap.Logger
	ctx         context.Context
	cancel      context.CancelFunc
}

// NewWorkerPool creates a new worker pool
func NewWorkerPool(numWorkers, jobQueueSize, resultQueueSize int, logger *zap.Logger) *WorkerPool {
	ctx, cancel := context.WithCancel(context.Background())

	return &WorkerPool{
		numWorkers:  numWorkers,
		jobsChan:    make(chan *Job, jobQueueSize),
		resultsChan: make(chan *Result, resultQueueSize),
		logger:      logger,
		ctx:         ctx,
		cancel:      cancel,
	}
}

// Start launches all workers
func (wp *WorkerPool) Start() {
	wp.logger.Info("starting worker pool", zap.Int("num_workers", wp.numWorkers))

	for i := 0; i < wp.numWorkers; i++ {
		worker := &Worker{
			id:          i,
			jobsChan:    wp.jobsChan,
			resultsChan: wp.resultsChan,
			logger:      wp.logger,
		}
		go worker.run(wp.ctx)
	}
}

// Stop gracefully stops all workers
func (wp *WorkerPool) Stop() {
	wp.logger.Info("stopping worker pool")
	wp.cancel()
	close(wp.jobsChan)
}

// SubmitJob adds a job to the queue (blocks if queue is full)
func (wp *WorkerPool) SubmitJob(ctx context.Context, job *Job) error {
	select {
	case wp.jobsChan <- job:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Results returns the results channel
func (wp *WorkerPool) Results() <-chan *Result {
	return wp.resultsChan
}
