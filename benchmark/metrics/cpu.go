package metrics

import (
	"os/exec"
	"strconv"
	"strings"
)

func GetCPUUsage(containerName string) (*CPUStats, error) {
	// Get CPU stats from docker stats (requires docker CLI)
	cmd := exec.Command("docker", "stats", "--no-stream", "--format", "{{.CPUPerc}}", containerName)
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
