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
	b.ResetTimer()

	var totalMessages atomic.Int64
	startTime := time.Now()

	cpuBefore, _ := metrics.GetCPUUsage(r.containerName)
	memBefore, _ := metrics.GetMemoryUsage(r.containerName)

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			for i := 0; i < messagesPerOp; i++ {
				conn, err := amqp091.Dial(r.ProxyURL)
				if err != nil {
					b.Errorf("Failed to connect: %v", err)
					return
				}

				if err := callback(conn); err != nil {
					conn.Close()
					b.Errorf("Callback failed: %v", err)
					return
				}

				conn.Close()
			}
			totalMessages.Add(int64(messagesPerOp))
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
