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

const (
	testQueue = "benchmark-queue"
	exchange  = "benchmark-exchange"
)

func setupQueue(connURL string) error {
	conn, err := amqp091.Dial(connURL)
	if err != nil {
		return err
	}
	defer conn.Close()

	ch, err := conn.Channel()
	if err != nil {
		return err
	}
	defer ch.Close()

	if err := ch.ExchangeDeclare(exchange, "direct", true, false, false, false, nil); err != nil {
		return err
	}

	q, err := ch.QueueDeclare(testQueue, true, false, false, false, nil)
	if err != nil {
		return err
	}

	if err := ch.QueueBind(q.Name, "", exchange, false, nil); err != nil {
		return err
	}

	// Purge any messages from previous runs. Without this, messages accumulate
	// across benchmark runs (nobody consumes them), eventually filling Docker's
	// disk and triggering RabbitMQ's disk-space alarm which blocks all publishers.
	_, err = ch.QueuePurge(testQueue, false)
	return err
}

func BenchmarkShortLived_AMQplex(b *testing.B) {
	reporter := metrics.NewReporter(os.Getenv("RESULTS_DIR"))
	b.Cleanup(func() {
		if err := reporter.Save(); err != nil {
			b.Logf("Failed to save results: %v", err)
		}
	})
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

	amqplexRunner.RunScenario(b, "short_lived", 1, 1024, func(conn *amqp091.Connection) error {
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
}

func BenchmarkShortLived_AMQProxy(b *testing.B) {
	reporter := metrics.NewReporter(os.Getenv("RESULTS_DIR"))
	b.Cleanup(func() {
		if err := reporter.Save(); err != nil {
			b.Logf("Failed to save results: %v", err)
		}
	})
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

	amqproxyRunner.RunScenario(b, "short_lived", 1, 1024, func(conn *amqp091.Connection) error {
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
}
