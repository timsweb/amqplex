package scenarios_test

import (
	"context"
	"os"
	"testing"
	"time"

	amqp091 "github.com/rabbitmq/amqp091-go"
	"github.com/timsweb/amqplex/benchmark/metrics"
	"github.com/timsweb/amqplex/benchmark/runner"
)

func BenchmarkHighConcurrency_AMQplex(b *testing.B) {
	reporter := metrics.NewReporter(os.Getenv("RESULTS_DIR"))
	amqplexRunner := runner.NewRunner(
		"amqp://localhost:5673",
		"benchmark-amqplex-1",
		metrics.ProxyAMQplex,
		reporter,
	)

	if err := amqplexRunner.WaitReady(20, 500*time.Millisecond); err != nil {
		b.Fatalf("AMQplex not ready: %v", err)
	}

	if err := setupQueue(amqplexRunner.ProxyURL); err != nil {
		b.Fatalf("Failed to setup queue: %v", err)
	}

	// SetParallelism controls concurrent goroutines in RunParallel; each
	// goroutine opens its own connection, so this tests concurrent connections.
	b.SetParallelism(10)

	amqplexRunner.RunScenario(b, "high_concurrency", 1, 1024, func(conn *amqp091.Connection) error {
		ch, err := conn.Channel()
		if err != nil {
			return err
		}
		defer ch.Close()

		body := make([]byte, 1024)
		return ch.PublishWithContext(context.Background(), exchange, "", false, false, amqp091.Publishing{
			Body: body,
		})
	})

	if err := reporter.Save(); err != nil {
		b.Logf("Failed to save results: %v", err)
	}
}

func BenchmarkHighConcurrency_AMQProxy(b *testing.B) {
	reporter := metrics.NewReporter(os.Getenv("RESULTS_DIR"))
	amqproxyRunner := runner.NewRunner(
		"amqp://localhost:5674",
		"benchmark-amqproxy-1",
		metrics.ProxyAMQProxy,
		reporter,
	)

	if err := amqproxyRunner.WaitReady(20, 500*time.Millisecond); err != nil {
		b.Fatalf("AMQProxy not ready: %v", err)
	}

	if err := setupQueue(amqproxyRunner.ProxyURL); err != nil {
		b.Fatalf("Failed to setup queue: %v", err)
	}

	b.SetParallelism(10)

	amqproxyRunner.RunScenario(b, "high_concurrency", 1, 1024, func(conn *amqp091.Connection) error {
		ch, err := conn.Channel()
		if err != nil {
			return err
		}
		defer ch.Close()

		body := make([]byte, 1024)
		return ch.PublishWithContext(context.Background(), exchange, "", false, false, amqp091.Publishing{
			Body: body,
		})
	})

	if err := reporter.Save(); err != nil {
		b.Logf("Failed to save results: %v", err)
	}
}
