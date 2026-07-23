package sysinfo

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

type SystemStats struct {
	Hostname   string  `json:"hostname"`
	OS         string  `json:"os"`
	Arch       string  `json:"arch"`
	CPUCount   int     `json:"cpu_count"`
	LoadAvg1   float64 `json:"load_avg_1"`
	MemTotalMB uint64  `json:"mem_total_mb"`
	MemFreeMB  uint64  `json:"mem_free_mb"`
	MemUsedMB  uint64  `json:"mem_used_mb"`
	DiskTotal  uint64  `json:"disk_total_bytes"`
	DiskFree   uint64  `json:"disk_free_bytes"`
}

func GetStats() (*SystemStats, error) {
	hostname, _ := os.Hostname()

	stats := &SystemStats{
		Hostname: hostname,
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		CPUCount: runtime.NumCPU(),
	}

	// 1. Read /proc/loadavg
	if data, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) > 0 {
			stats.LoadAvg1, _ = strconv.ParseFloat(fields[0], 64)
		}
	}

	// 2. Read /proc/meminfo
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		lines := strings.Split(string(data), "\n")
		var memTotal, memAvailable uint64
		for _, line := range lines {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				if fields[0] == "MemTotal:" {
					memTotal, _ = strconv.ParseUint(fields[1], 10, 64)
				} else if fields[0] == "MemAvailable:" {
					memAvailable, _ = strconv.ParseUint(fields[1], 10, 64)
				}
			}
		}
		stats.MemTotalMB = memTotal / 1024
		stats.MemFreeMB = memAvailable / 1024
		stats.MemUsedMB = stats.MemTotalMB - stats.MemFreeMB
	}

	// 3. Statfs for Disk Usage
	var stat syscall.Statfs_t
	if err := syscall.Statfs("/", &stat); err == nil {
		stats.DiskTotal = stat.Blocks * uint64(stat.Bsize)
		stats.DiskFree = stat.Bavail * uint64(stat.Bsize)
	}

	return stats, nil
}

func (s *SystemStats) Summary() string {
	return fmt.Sprintf("Host: %s (%s/%s, %d CPUs) | Load: %.2f | Mem: %dMB/%dMB used | Disk Free: %dGB",
		s.Hostname, s.OS, s.Arch, s.CPUCount, s.LoadAvg1,
		s.MemUsedMB, s.MemTotalMB, s.DiskFree/(1024*1024*1024))
}
