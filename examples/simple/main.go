package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/confluentinc/confluent-kafka-go/v2/kafka"
	"github.com/vlab-research/fly/burrow"
	"go.uber.org/zap"
)

// MyData represents the message structure we're processing
type MyData struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func main() {
	// Step 1: Create a standard Kafka consumer
	// IMPORTANT: Set enable.auto.commit to false - Burrow handles commits
	consumer, err := kafka.NewConsumer(&kafka.ConfigMap{
		"bootstrap.servers":  "localhost:9092",     // Your Kafka broker address
		"group.id":           "burrow-example",     // Consumer group ID
		"auto.offset.reset":  "earliest",           // Start from beginning if no offset
		"enable.auto.commit": false,                // CRITICAL: Let Burrow handle commits
	})
	if err != nil {
		log.Fatalf("Failed to create consumer: %v", err)
	}
	defer consumer.Close()

	// Subscribe to your Kafka topics
	err = consumer.SubscribeTopics([]string{"my-topic"}, nil)
	if err != nil {
		log.Fatalf("Failed to subscribe to topics: %v", err)
	}

	log.Println("Successfully connected to Kafka and subscribed to topics")

	// Step 2: Create a logger (required for Burrow)
	// Use NewProduction() for production, NewDevelopment() for debugging
	logger, err := zap.NewProduction()
	if err != nil {
		log.Fatalf("Failed to create logger: %v", err)
	}
	defer logger.Sync()

	// Step 3: Create Burrow configuration
	// Start with defaults and customize as needed
	config := burrow.DefaultConfig(logger)
	config.NumWorkers = 100 // Process 100 messages concurrently

	// Optional: Customize other settings
	// config.CommitInterval = 10 * time.Second    // Commit every 10 seconds
	// config.CommitBatchSize = 1000                // OR commit every 1000 messages
	// config.MaxConsecutiveErrors = 5              // Halt after 5 consecutive errors
	// config.MaxRetries = 3                        // Retry failed messages 3 times

	logger.Info("burrow configuration",
		zap.Int("num_workers", config.NumWorkers),
		zap.Duration("commit_interval", config.CommitInterval),
		zap.Int("max_consecutive_errors", config.MaxConsecutiveErrors))

	// Step 4: Create the Burrow pool
	pool, err := burrow.NewPool(consumer, config)
	if err != nil {
		log.Fatalf("Failed to create pool: %v", err)
	}

	logger.Info("burrow pool created successfully")

	// Step 5: Define your message processing function
	// This is where you implement your business logic
	processFunc := func(ctx context.Context, msg *kafka.Message) error {
		// Parse the message
		var data MyData
		if err := json.Unmarshal(msg.Value, &data); err != nil {
			logger.Error("failed to unmarshal message",
				zap.Error(err),
				zap.String("value", string(msg.Value)))
			return err // Returning error will trigger retry
		}

		// Do your I/O-heavy work here
		// Examples:
		// - Call external APIs
		// - Write to database
		// - Process files
		// - Send emails
		// - etc.

		// For this example, we'll simulate some work
		time.Sleep(50 * time.Millisecond)

		// Log successful processing
		logger.Info("successfully processed message",
			zap.String("id", data.ID),
			zap.String("name", data.Name),
			zap.Int32("partition", msg.TopicPartition.Partition),
			zap.Int64("offset", int64(msg.TopicPartition.Offset)))

		// Return nil to indicate success
		return nil
	}

	// Step 6: Setup graceful shutdown handling
	// This ensures we commit final offsets before exiting
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Listen for interrupt signals (Ctrl+C, SIGTERM)
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Handle shutdown signal in background goroutine
	go func() {
		sig := <-sigChan
		logger.Info("shutdown signal received",
			zap.String("signal", sig.String()))
		cancel() // This will stop the pool
	}()

	// Step 7: Run the pool
	// This blocks until the context is cancelled
	logger.Info("starting message processing",
		zap.Int("workers", config.NumWorkers))

	if err := pool.Run(ctx, processFunc); err != nil && err != context.Canceled {
		log.Fatalf("Pool error: %v", err)
	}

	logger.Info("shutdown complete - all offsets committed")
}

/*
How to run this example:

1. Start Kafka locally (using Docker):
   docker run -d --name kafka \
     -p 9092:9092 \
     -e KAFKA_ENABLE_KRAFT=yes \
     -e KAFKA_BROKER_ID=1 \
     -e KAFKA_CFG_PROCESS_ROLES=broker,controller \
     -e KAFKA_CFG_CONTROLLER_LISTENER_NAMES=CONTROLLER \
     -e KAFKA_CFG_LISTENERS=PLAINTEXT://:9092,CONTROLLER://:9093 \
     -e KAFKA_CFG_LISTENER_SECURITY_PROTOCOL_MAP=CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT \
     -e KAFKA_CFG_ADVERTISED_LISTENERS=PLAINTEXT://localhost:9092 \
     -e KAFKA_CFG_CONTROLLER_QUORUM_VOTERS=1@localhost:9093 \
     bitnami/kafka:latest

2. Create a test topic:
   docker exec -it kafka kafka-topics.sh \
     --create \
     --bootstrap-server localhost:9092 \
     --topic my-topic \
     --partitions 3 \
     --replication-factor 1

3. Produce some test messages:
   docker exec -it kafka kafka-console-producer.sh \
     --bootstrap-server localhost:9092 \
     --topic my-topic

   Then type:
   {"id": "1", "name": "Alice"}
   {"id": "2", "name": "Bob"}
   {"id": "3", "name": "Charlie"}

4. Run this example:
   go run examples/simple/main.go

5. Watch the logs to see messages being processed in parallel!

6. Press Ctrl+C to gracefully shutdown

Key Features Demonstrated:
- Parallel processing (100 workers)
- At-least-once delivery (messages never lost)
- Ordered offset commits (no gaps)
- Graceful shutdown with final commit
- Error handling with retry logic
- Production-ready logging

Performance Benefits:
- Without Burrow: 100 messages × 50ms = 5 seconds
- With Burrow (100 workers): ~50-100ms total
- 50-100x throughput improvement!
*/
