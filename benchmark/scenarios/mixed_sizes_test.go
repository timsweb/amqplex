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

type MessageSize struct {
	Name string
	Size int
}

var messageSizes = []MessageSize{
	{"small", 1024},   // 1KB
	{"medium", 10240}, // 10KB
	{"large", 102400}, // 100KB
}

func BenchmarkMixedSizes_AMQplex(b *testing.B) {
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

	for _, msgSize := range messageSizes {
		b.Run(msgSize.Name, func(b *testing.B) {
			amqplexRunner.RunScenario(b, "mixed_sizes_"+msgSize.Name, 1, msgSize.Size, func(conn *amqp091.Connection) error {
				ch, err := conn.Channel()
				if err != nil {
					return err
				}
				defer ch.Close()

				body := make([]byte, msgSize.Size)
				return ch.PublishWithContext(context.Background(), exchange, "", false, false, amqp091.Publishing{
					Body: body,
				})
			})
		})
	}

	if err := reporter.Save(); err != nil {
		b.Logf("Failed to save results: %v", err)
	}
}

func BenchmarkMixedSizes_AMQProxy(b *testing.B) {
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

	for _, msgSize := range messageSizes {
		b.Run(msgSize.Name, func(b *testing.B) {
			amqproxyRunner.RunScenario(b, "mixed_sizes_"+msgSize.Name, 1, msgSize.Size, func(conn *amqp091.Connection) error {
				ch, err := conn.Channel()
				if err != nil {
					return err
				}
				defer ch.Close()

				body := make([]byte, msgSize.Size)
				return ch.PublishWithContext(context.Background(), exchange, "", false, false, amqp091.Publishing{
					Body: body,
				})
			})
		})
	}

	if err := reporter.Save(); err != nil {
		b.Logf("Failed to save results: %v", err)
	}
}
