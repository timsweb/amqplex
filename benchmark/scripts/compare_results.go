package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"

	"github.com/timsweb/amqplex/benchmark/metrics"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run benchmark/scripts/compare_results.go <results-dir>")
		os.Exit(1)
	}

	resultsDir := os.Args[1]

	// Read all individual result files (skip combined_results.json).
	// Go benchmarks call the function multiple times with increasing N;
	// each call writes a new file. We want the last result per proxy+scenario
	// (highest N, most accurate).
	entries, err := filepath.Glob(filepath.Join(resultsDir, "*.json"))
	if err != nil || len(entries) == 0 {
		fmt.Fprintf(os.Stderr, "No result files found in %s\n", resultsDir)
		os.Exit(1)
	}
	sort.Strings(entries) // filenames embed timestamps, so alphabetical = chronological

	// latest holds the final result for each proxy+scenario key.
	latest := make(map[string]metrics.BenchmarkResult)

	for _, path := range entries {
		if filepath.Base(path) == "combined_results.json" {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		// Each file holds a single BenchmarkResult object.
		var result metrics.BenchmarkResult
		if err := json.Unmarshal(data, &result); err != nil {
			continue
		}
		key := string(result.Proxy) + "/" + result.Scenario
		latest[key] = result
	}

	if len(latest) == 0 {
		fmt.Fprintln(os.Stderr, "No valid results found.")
		os.Exit(1)
	}

	// Collect unique scenarios (sorted).
	scenarioSet := make(map[string]struct{})
	for _, r := range latest {
		scenarioSet[r.Scenario] = struct{}{}
	}
	scenarios := make([]string, 0, len(scenarioSet))
	for s := range scenarioSet {
		scenarios = append(scenarios, s)
	}
	sort.Strings(scenarios)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "Scenario\tProxy\tMessages\tDuration\tThroughput/s\tMemory (MB)\n")
	fmt.Fprintf(w, "--------\t-----\t--------\t--------\t------------\t------------\n")

	var allResults []metrics.BenchmarkResult
	for _, r := range latest {
		allResults = append(allResults, r)
	}

	for _, scenarioName := range scenarios {
		amqplex := latest["amqplex/"+scenarioName]
		amqproxy := latest["amqproxy/"+scenarioName]

		hasAmqplex := amqplex.Proxy != ""
		hasAmqproxy := amqproxy.Proxy != ""

		if hasAmqplex {
			memoryMB := float64(amqplex.MemoryStats.CurrentRSS) / 1024
			fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%.0f\t%.1f\n",
				scenarioName, amqplex.Proxy, amqplex.Messages,
				amqplex.Duration, amqplex.Throughput, memoryMB)
		}

		if hasAmqproxy {
			memoryMB := float64(amqproxy.MemoryStats.CurrentRSS) / 1024
			fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%.0f\t%.1f\n",
				scenarioName, amqproxy.Proxy, amqproxy.Messages,
				amqproxy.Duration, amqproxy.Throughput, memoryMB)
		}

		if hasAmqplex && hasAmqproxy {
			speedup := amqplex.Throughput / amqproxy.Throughput
			if speedup >= 1 {
				fmt.Fprintf(w, "\t→ AMQplex is %.2fx faster\t\t\t\t\n\n", speedup)
			} else {
				fmt.Fprintf(w, "\t→ AMQProxy is %.2fx faster\t\t\t\t\n\n", 1/speedup)
			}
		} else {
			fmt.Fprintln(w)
		}
	}
	w.Flush()
}
