package metrics

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func GetMemoryUsage(containerName string) (*MemoryStats, error) {
	// Get memory stats from docker stats
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "stats", "--no-stream", "--format", "{{.MemUsage}}", containerName)
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	memUsageStr := strings.TrimSpace(string(output))
	parts := strings.Split(memUsageStr, "/")
	if len(parts) == 0 {
		return nil, nil
	}

	usagePart := strings.TrimSpace(parts[0])
	var rssKB int64

	if strings.Contains(usagePart, "GiB") {
		gib, err := strconv.ParseFloat(strings.TrimSuffix(usagePart, "GiB"), 64)
		if err == nil {
			rssKB = int64(gib * 1024 * 1024)
		}
	} else if strings.Contains(usagePart, "MiB") {
		mib, err := strconv.ParseFloat(strings.TrimSuffix(usagePart, "MiB"), 64)
		if err == nil {
			rssKB = int64(mib * 1024)
		}
	} else if strings.Contains(usagePart, "KiB") {
		kib, err := strconv.ParseFloat(strings.TrimSuffix(usagePart, "KiB"), 64)
		if err == nil {
			rssKB = int64(kib)
		}
	}

	return &MemoryStats{
		CurrentRSS: rssKB,
		MaxRSS:     rssKB,
	}, nil
}
