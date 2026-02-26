package runner

import (
	"fmt"
	"net"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	amqp091 "github.com/rabbitmq/amqp091-go"
	"github.com/timsweb/amqplex/benchmark/metrics"
)


// opTimeout caps the entire per-iteration operation (TCP connect + AMQP
// handshake + channel open + publish + close). The TCP dial timeout alone is
// not sufficient: a server can accept the TCP connection but stall during the
// AMQP handshake, leaving the goroutine blocked indefinitely inside
// b.RunParallel. A leaked goroutine is acceptable here since the process exits
// after the benchmark completes.
const opTimeout = 15 * time.Second

type BenchmarkRunner struct {
	ProxyURL      string
	tcpAddr       string
	containerName string
	proxyType     metrics.ProxyType
	reporter      *metrics.Reporter
}

func NewRunner(proxyURL, containerName string, proxyType metrics.ProxyType, reporter *metrics.Reporter) *BenchmarkRunner {
	// Parse TCP address (host:port) from AMQP URL for readiness checks.
	tcpAddr := proxyURL
	if u, err := url.Parse(proxyURL); err == nil {
		host := u.Hostname()
		port := u.Port()
		if port == "" {
			port = "5672"
		}
		tcpAddr = net.JoinHostPort(host, port)
	}

	return &BenchmarkRunner{
		ProxyURL:      proxyURL,
		tcpAddr:       tcpAddr,
		containerName: containerName,
		proxyType:     proxyType,
		reporter:      reporter,
	}
}

func (r *BenchmarkRunner) RunScenario(b *testing.B, scenarioName string, messagesPerOp int, messageSize int, callback func(conn *amqp091.Connection) error) {
	// Warmup: establish the upstream connection before measurement starts so
	// the first timed iteration doesn't pay the extra cost of dialling RabbitMQ.
	if conn, err := amqp091.Dial(r.ProxyURL); err == nil {
		conn.Close()
	}

	b.ResetTimer()

	var totalMessages atomic.Int64

	cpuBefore, _ := metrics.GetCPUUsage(r.containerName)
	memBefore, _ := metrics.GetMemoryUsage(r.containerName)

	startTime := time.Now()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			type opResult struct{ err error }
			ch := make(chan opResult, 1)
			go func() {
				conn, err := amqp091.Dial(r.ProxyURL)
				if err != nil {
					ch <- opResult{err}
					return
				}
				err = callback(conn)
				conn.Close()
				ch <- opResult{err}
			}()

			select {
			case res := <-ch:
				if res.err != nil {
					b.Errorf("operation failed: %v", res.err)
					return
				}
				totalMessages.Add(int64(messagesPerOp))
			case <-time.After(opTimeout):
				b.Errorf("operation timed out after %s", opTimeout)
				return
			}
		}
	})

	duration := time.Since(startTime)
	total := totalMessages.Load()

	cpuAfter, _ := metrics.GetCPUUsage(r.containerName)
	memAfter, _ := metrics.GetMemoryUsage(r.containerName)

	var avgCPU float64
	if cpuBefore != nil && cpuAfter != nil {
		avgCPU = (cpuBefore.CPUPercent + cpuAfter.CPUPercent) / 2
	}

	var memStats metrics.MemoryStats
	if memAfter != nil {
		memStats = metrics.MemoryStats{
			CurrentRSS: memAfter.CurrentRSS,
			MaxRSS:     memAfter.MaxRSS,
		}
	}
	_ = memBefore // collected for potential delta use

	result := metrics.BenchmarkResult{
		Proxy:      r.proxyType,
		Scenario:   scenarioName,
		Messages:   total,
		Duration:   duration,
		Throughput: float64(total) / duration.Seconds(),
		CPUStats: metrics.CPUStats{
			CPUPercent: avgCPU,
		},
		MemoryStats: memStats,
		Timestamp:   time.Now(),
	}

	r.reporter.AddResult(result)

	b.ReportMetric(float64(total)/duration.Seconds(), "msg/sec")
}

// WaitReady polls the proxy's TCP port until it accepts connections.
func (r *BenchmarkRunner) WaitReady(maxAttempts int, delay time.Duration) error {
	for i := 0; i < maxAttempts; i++ {
		conn, err := net.DialTimeout("tcp", r.tcpAddr, delay)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(delay)
	}
	return fmt.Errorf("proxy not ready after %d attempts", maxAttempts)
}
