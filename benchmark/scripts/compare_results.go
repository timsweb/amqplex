package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/timsweb/amqplex/benchmark/metrics"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run benchmark/scripts/compare_results.go <results-dir>")
		os.Exit(1)
	}

	resultsDir := os.Args[1]
	combinedPath := filepath.Join(resultsDir, "combined_results.json")

	data, err := os.ReadFile(combinedPath)
	if err != nil {
		fmt.Printf("Failed to read results: %v\n", err)
		os.Exit(1)
	}

	var results []metrics.BenchmarkResult
	if err := json.Unmarshal(data, &results); err != nil {
		fmt.Printf("Failed to parse results: %v\n", err)
		os.Exit(1)
	}

	scenarios := make(map[string][]metrics.BenchmarkResult)
	for _, result := range results {
		scenarios[result.Scenario] = append(scenarios[result.Scenario], result)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "Scenario\tProxy\tMessages\tDuration\tThroughput/s\tCPU%%\tMemory (MB)\n")
	fmt.Fprintf(w, "--------\t-----\t--------\t--------\t------------\t-----\t------------\n")

	for scenarioName, scenarioResults := range scenarios {
		var amqplex, amqproxy *metrics.BenchmarkResult
		for i := range scenarioResults {
			if scenarioResults[i].Proxy == metrics.ProxyAMQplex {
				amqplex = &scenarioResults[i]
			} else if scenarioResults[i].Proxy == metrics.ProxyAMQProxy {
				amqproxy = &scenarioResults[i]
			}
		}

		if amqplex != nil {
			memoryMB := float64(amqplex.MemoryStats.CurrentRSS) / 1024
			fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%.2f\t%.2f\t%.2f\n",
				scenarioName, amqplex.Proxy, amqplex.Messages,
				amqplex.Duration, amqplex.Throughput, amqplex.CPUStats.CPUPercent, memoryMB)
		}

		if amqproxy != nil {
			memoryMB := float64(amqproxy.MemoryStats.CurrentRSS) / 1024
			fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%.2f\t%.2f\t%.2f\n",
				scenarioName, amqproxy.Proxy, amqproxy.Messages,
				amqproxy.Duration, amqproxy.Throughput, amqproxy.CPUStats.CPUPercent, memoryMB)
		}

		if amqplex != nil && amqproxy != nil {
			speedup := amqplex.Throughput / amqproxy.Throughput
			memoryDiff := float64(amqproxy.MemoryStats.CurrentRSS-amqplex.MemoryStats.CurrentRSS) / 1024
			cpuDiff := amqproxy.CPUStats.CPUPercent - amqplex.CPUStats.CPUPercent

			if speedup > 1 {
				fmt.Fprintf(w, "  → AMQplex is %.2fx faster, %.2f%% less CPU, %.2f MB less memory\n\n",
					speedup, cpuDiff, memoryDiff)
			} else if speedup < 1 {
				fmt.Fprintf(w, "  → AMQProxy is %.2fx faster, %.2f%% less CPU, %.2f MB less memory\n\n",
					1/speedup, -cpuDiff, -memoryDiff)
			}
		}
	}
	w.Flush()

	fmt.Println("\n=== Aggregate Statistics ===\n")
	printAggregate("AMQplex", results, metrics.ProxyAMQplex)
	printAggregate("AMQProxy", results, metrics.ProxyAMQProxy)
}

func printAggregate(name string, results []metrics.BenchmarkResult, proxyType metrics.ProxyType) {
	var totalMessages int64
	var totalDuration float64
	var totalCPU float64
	var totalMemoryKB int64
	var count int

	for _, r := range results {
		if r.Proxy == proxyType {
			totalMessages += r.Messages
			totalDuration += r.Duration.Seconds()
			totalCPU += r.CPUStats.CPUPercent
			totalMemoryKB += r.MemoryStats.CurrentRSS
			count++
		}
	}

	if count == 0 {
		return
	}

	avgThroughput := float64(totalMessages) / totalDuration
	avgCPU := totalCPU / float64(count)
	avgMemoryMB := float64(totalMemoryKB) / float64(count) / 1024

	fmt.Printf("%s:\n", name)
	fmt.Printf("  Total Messages: %d\n", totalMessages)
	fmt.Printf("  Avg Throughput: %.2f msg/sec\n", avgThroughput)
	fmt.Printf("  Avg CPU Usage: %.2f%%\n", avgCPU)
	fmt.Printf("  Avg Memory: %.2f MB\n", avgMemoryMB)
	fmt.Println()
}
