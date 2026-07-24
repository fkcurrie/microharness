package sysinfo

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

type GPUStats struct {
	Name        string `json:"name"`
	UtilPct     int    `json:"util_pct"`
	VRAMUsedMB  uint64 `json:"vram_used_mb"`
	VRAMTotalMB uint64 `json:"vram_total_mb"`
}

type SystemStats struct {
	Hostname   string    `json:"hostname"`
	OS         string    `json:"os"`
	Arch       string    `json:"arch"`
	CPUCount   int       `json:"cpu_count"`
	LoadAvg1   float64   `json:"load_avg_1"`
	MemTotalMB uint64    `json:"mem_total_mb"`
	MemFreeMB  uint64    `json:"mem_free_mb"`
	MemUsedMB  uint64    `json:"mem_used_mb"`
	DiskTotal  uint64    `json:"disk_total_bytes"`
	DiskFree   uint64    `json:"disk_free_bytes"`
	GPU        *GPUStats `json:"gpu,omitempty"`
}

func GetGPUStats() *GPUStats {
	// 1. Try nvidia-smi first for NVIDIA hardware
	if npath, err := exec.LookPath("nvidia-smi"); err == nil && npath != "" {
		cmd := exec.Command(npath, "--query-gpu=name,utilization.gpu,memory.used,memory.total", "--format=csv,noheader,nounits")
		out, err := cmd.Output()
		if err == nil {
			parts := strings.Split(strings.TrimSpace(string(out)), ",")
			if len(parts) >= 4 {
				name := strings.TrimSpace(parts[0])
				util, _ := strconv.Atoi(strings.TrimSpace(parts[1]))
				used, _ := strconv.ParseUint(strings.TrimSpace(parts[2]), 10, 64)
				total, _ := strconv.ParseUint(strings.TrimSpace(parts[3]), 10, 64)
				return &GPUStats{
					Name:        name,
					UtilPct:     util,
					VRAMUsedMB:  used,
					VRAMTotalMB: total,
				}
			}
		}
	}

	// 2. Check Linux DRM sysfs (/sys/class/drm/card*/device/) for AMD / Intel / generic GPUs
	matches, err := filepath.Glob("/sys/class/drm/card*/device/gpu_busy_percent")
	if err == nil && len(matches) > 0 {
		cardPath := filepath.Dir(matches[0])
		var util int
		var vramUsedMB, vramTotalMB uint64

		if data, err := os.ReadFile(filepath.Join(cardPath, "gpu_busy_percent")); err == nil {
			util, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		}

		if data, err := os.ReadFile(filepath.Join(cardPath, "mem_info_vram_used")); err == nil {
			usedBytes, _ := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
			vramUsedMB = usedBytes / (1024 * 1024)
		}

		if data, err := os.ReadFile(filepath.Join(cardPath, "mem_info_vram_total")); err == nil {
			totalBytes, _ := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
			vramTotalMB = totalBytes / (1024 * 1024)
		}

		// Obtain GPU Model Name via lspci
		name := "AMD/Generic GPU"
		if lspci, err := exec.LookPath("lspci"); err == nil && lspci != "" {
			cmd := exec.Command(lspci)
			out, err := cmd.Output()
			if err == nil {
				lines := strings.Split(string(out), "\n")
				for _, line := range lines {
					lower := strings.ToLower(line)
					if strings.Contains(lower, "vga") || strings.Contains(lower, "3d") || strings.Contains(lower, "display") {
						idx := strings.Index(line, ": ")
						if idx != -1 {
							cleanName := line[idx+2:]
							if revIdx := strings.Index(cleanName, " (rev"); revIdx != -1 {
								cleanName = cleanName[:revIdx]
							}
							name = strings.TrimSpace(cleanName)
							break
						}
					}
				}
			}
		}

		return &GPUStats{
			Name:        name,
			UtilPct:     util,
			VRAMUsedMB:  vramUsedMB,
			VRAMTotalMB: vramTotalMB,
		}
	}

	return nil
}

func GetStats() (*SystemStats, error) {
	hostname, _ := os.Hostname()

	stats := &SystemStats{
		Hostname: hostname,
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		CPUCount: runtime.NumCPU(),
		GPU:      GetGPUStats(),
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
	gpuStr := ""
	if s.GPU != nil && s.GPU.Name != "" {
		if s.GPU.VRAMTotalMB > 0 {
			gpuStr = fmt.Sprintf(" | GPU: %s (%d%% util, VRAM %dMB/%dMB)", s.GPU.Name, s.GPU.UtilPct, s.GPU.VRAMUsedMB, s.GPU.VRAMTotalMB)
		} else {
			gpuStr = fmt.Sprintf(" | GPU: %s (%d%% util)", s.GPU.Name, s.GPU.UtilPct)
		}
	}
	return fmt.Sprintf("Host: %s (%s/%s, %d CPUs) | Load: %.2f | Mem: %dMB/%dMB used | Disk Free: %dGB%s",
		s.Hostname, s.OS, s.Arch, s.CPUCount, s.LoadAvg1,
		s.MemUsedMB, s.MemTotalMB, s.DiskFree/(1024*1024*1024), gpuStr)
}

