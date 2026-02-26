package metrics

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func GetCPUUsage(containerName string) (*CPUStats, error) {
	// Get CPU stats from docker stats (requires docker CLI)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "stats", "--no-stream", "--format", "{{.CPUPerc}}", containerName)
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	cpuPercentStr := strings.TrimSpace(string(output))
	cpuPercentStr = strings.TrimSuffix(cpuPercentStr, "%")

	cpuPercent, err := strconv.ParseFloat(cpuPercentStr, 64)
	if err != nil {
		return nil, err
	}

	return &CPUStats{
		CPUPercent: cpuPercent,
	}, nil
}
