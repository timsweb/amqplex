package metrics

import "time"

type ProxyType string

const (
	ProxyAMQplex  ProxyType = "amqplex"
	ProxyAMQProxy ProxyType = "amqproxy"
)

type BenchmarkResult struct {
	Proxy       ProxyType     `json:"proxy"`
	Scenario    string        `json:"scenario"`
	Messages    int64         `json:"messages"`
	Duration    time.Duration `json:"duration"`
	Throughput  float64       `json:"throughput_msg_per_sec"`
	CPUStats    CPUStats      `json:"cpu"`
	MemoryStats MemoryStats   `json:"memory"`
	Timestamp   time.Time     `json:"timestamp"`
}

type CPUStats struct {
	UserTime   float64 `json:"user_time_s"`
	SystemTime float64 `json:"system_time_s"`
	TotalCPU   float64 `json:"total_cpu_s"`
	CPUPercent float64 `json:"cpu_percent"`
}

type MemoryStats struct {
	MaxRSS       int64   `json:"max_rss_kb"`
	CurrentRSS   int64   `json:"current_rss_kb"`
	AllocatedMB  float64 `json:"allocated_mb"`
	NumGC        uint32  `json:"num_gc"`
	PauseTotalNs uint64  `json:"pause_total_ns"`
}
