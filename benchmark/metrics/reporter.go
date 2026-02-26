package metrics

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Reporter struct {
	resultsDir string
	// results is keyed by "proxy/scenario" and always holds the latest value.
	// The benchmark framework calls the benchmark function multiple times with
	// increasing N for calibration; overwriting ensures only the final
	// (highest-N, steady-state) result is saved.
	results map[string]BenchmarkResult
}

func NewReporter(resultsDir string) *Reporter {
	return &Reporter{
		resultsDir: resultsDir,
		results:    make(map[string]BenchmarkResult),
	}
}

func (r *Reporter) AddResult(result BenchmarkResult) {
	key := string(result.Proxy) + "/" + result.Scenario
	r.results[key] = result
}

func (r *Reporter) Save() error {
	if err := os.MkdirAll(r.resultsDir, 0755); err != nil {
		return err
	}

	for _, result := range r.results {
		filename := fmt.Sprintf("%s_%s_%s.json",
			result.Proxy,
			result.Scenario,
			result.Timestamp.Format("20060102-150405"))

		filePath := filepath.Join(r.resultsDir, filename)

		data, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return err
		}

		if err := os.WriteFile(filePath, data, 0644); err != nil {
			return err
		}
	}

	combinedPath := filepath.Join(r.resultsDir, "combined_results.json")
	vals := make([]BenchmarkResult, 0, len(r.results))
	for _, v := range r.results {
		vals = append(vals, v)
	}
	data, err := json.MarshalIndent(vals, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(combinedPath, data, 0644)
}

func (r *Reporter) PrintSummary() {
	scenarios := make(map[string]map[ProxyType][]BenchmarkResult)
	for _, result := range r.results {
		if scenarios[result.Scenario] == nil {
			scenarios[result.Scenario] = make(map[ProxyType][]BenchmarkResult)
		}
		scenarios[result.Scenario][result.Proxy] = append(scenarios[result.Scenario][result.Proxy], result)
	}

	fmt.Println("\n=== Benchmark Summary ===")
	for scenario, proxies := range scenarios {
		fmt.Printf("Scenario: %s\n", scenario)
		fmt.Printf("%-12s %-15s %-15s %-15s %-15s\n",
			"Proxy", "Messages", "Duration", "Throughput/s", "CPU%")
		fmt.Println(strings.Repeat("-", 80))

		for proxy, results := range proxies {
			if len(results) == 0 {
				continue
			}

			var totalMessages int64
			var totalDuration time.Duration
			var totalCPU float64

			for _, r := range results {
				totalMessages += r.Messages
				totalDuration += r.Duration
				totalCPU += r.CPUStats.CPUPercent
			}

			avgCPU := totalCPU / float64(len(results))
			throughput := float64(totalMessages) / totalDuration.Seconds()

			fmt.Printf("%-12s %-15d %-15s %-15.2f %-15.2f\n",
				proxy,
				totalMessages,
				totalDuration,
				throughput,
				avgCPU)
		}
		fmt.Println()
	}
}
