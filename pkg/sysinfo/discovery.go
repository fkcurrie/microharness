package sysinfo

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type DiscoveredHost struct {
	IP              string `json:"ip"`
	Port22Open      bool   `json:"port_22_open"`
	PasswordlessSSH bool   `json:"passwordless_ssh"`
	Hostname        string `json:"hostname"`
	User            string `json:"user"`
	Interface       string `json:"interface"`
}

func GetDefaultSSHUser() string {
	if u := os.Getenv("USER"); u != "" && u != "root" {
		return u
	}
	if u := os.Getenv("LOGNAME"); u != "" && u != "root" {
		return u
	}
	if usr, err := user.Current(); err == nil && usr.Username != "" && usr.Username != "root" {
		return usr.Username
	}
	return "fcurrie"
}

func generateSubnetIPs(ipNet *net.IPNet, maxCandidates int) []string {
	var ips []string
	ip := ipNet.IP.To4()
	if ip == nil {
		return ips
	}

	mask := ipNet.Mask
	start := binary.BigEndian.Uint32(ip) & binary.BigEndian.Uint32(mask)
	end := start | ^binary.BigEndian.Uint32(mask)

	count := 0
	for current := start + 1; current < end; current++ {
		candidateIP := make(net.IP, 4)
		binary.BigEndian.PutUint32(candidateIP, current)
		ips = append(ips, candidateIP.String())
		count++
		if count >= maxCandidates {
			break
		}
	}
	return ips
}

func DiscoverNetworkTargets(ctx context.Context, defaultUser string) ([]DiscoveredHost, error) {
	if defaultUser == "" || defaultUser == "root" {
		defaultUser = GetDefaultSSHUser()
	}

	candMap := make(map[string]string) // ip -> interface/source

	// Priority candidates: Known PXE server and active subnet targets
	candMap["192.168.100.200"] = "pxe-net"
	candMap["192.168.4.61"] = "known-target"

	// 1. Inspect ALL system network interfaces (WiFi, Ethernet, Bridges)
	ifaces, err := net.Interfaces()
	if err == nil {
		for _, iface := range ifaces {
			if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
				continue
			}

			ifaceType := "Ethernet"
			if strings.HasPrefix(iface.Name, "wl") || strings.Contains(iface.Name, "wifi") || strings.Contains(iface.Name, "wlan") {
				ifaceType = "WiFi"
			}
			ifaceLabel := fmt.Sprintf("%s (%s)", iface.Name, ifaceType)

			addrs, err := iface.Addrs()
			if err != nil {
				continue
			}

			for _, addr := range addrs {
				ipNet, ok := addr.(*net.IPNet)
				if !ok || ipNet.IP.IsLoopback() || ipNet.IP.To4() == nil {
					continue
				}

				// Generate all subnet IPs (supports /16 to /28, e.g. /22 = 1022 hosts)
				subnetIPs := generateSubnetIPs(ipNet, 1024)
				for _, targetIP := range subnetIPs {
					if _, exists := candMap[targetIP]; !exists {
						candMap[targetIP] = ifaceLabel
					}
				}
			}
		}
	}

	// 2. Parse ARP / ip neighbor table across all interfaces
	if out, err := exec.CommandContext(ctx, "ip", "neighbor").CombinedOutput(); err == nil {
		lines := strings.Split(string(out), "\n")
		for _, line := range lines {
			fields := strings.Fields(line)
			if len(fields) >= 3 {
				ip := fields[0]
				if net.ParseIP(ip) != nil && !strings.HasPrefix(ip, "127.") {
					dev := "arp-neighbor"
					for i, f := range fields {
						if f == "dev" && i+1 < len(fields) {
							dev = fmt.Sprintf("arp (%s)", fields[i+1])
						}
					}
					candMap[ip] = dev
				}
			}
		}
	}

	// 3. Parse Default Gateways from ip route
	if out, err := exec.CommandContext(ctx, "ip", "route").CombinedOutput(); err == nil {
		lines := strings.Split(string(out), "\n")
		for _, line := range lines {
			fields := strings.Fields(line)
			for i, f := range fields {
				if f == "via" && i+1 < len(fields) {
					gwIP := fields[i+1]
					if net.ParseIP(gwIP) != nil && !strings.HasPrefix(gwIP, "127.") {
						candMap[gwIP] = "default-gateway"
					}
				}
			}
		}
	}

	// 4. Parse ~/.ssh/known_hosts for previously visited remote hosts
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		khPath := filepath.Join(home, ".ssh", "known_hosts")
		if data, err := os.ReadFile(khPath); err == nil {
			lines := strings.Split(string(data), "\n")
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "|") {
					continue
				}
				fields := strings.Fields(line)
				if len(fields) > 0 {
					hostToken := fields[0]
					hostToken = strings.Split(hostToken, ",")[0]
					hostToken = strings.TrimPrefix(hostToken, "[")
					hostToken = strings.Split(hostToken, "]")[0]
					if net.ParseIP(hostToken) != nil && !strings.HasPrefix(hostToken, "127.") {
						if _, exists := candMap[hostToken]; !exists {
							candMap[hostToken] = "ssh-known-hosts"
						}
					}
				}
			}
		}
	}

	// 5. Parse mDNS / Avahi SSH Services
	if out, err := exec.CommandContext(ctx, "avahi-browse", "-rt", "-p", "_ssh._tcp").CombinedOutput(); err == nil {
		lines := strings.Split(string(out), "\n")
		for _, line := range lines {
			fields := strings.Split(line, ";")
			if len(fields) >= 8 {
				targetIP := fields[7]
				if net.ParseIP(targetIP) != nil && !strings.HasPrefix(targetIP, "127.") {
					candMap[targetIP] = "mDNS-avahi"
				}
			}
		}
	}

	// 6. Run fast Nmap discovery if nmap binary is available
	if nmapPath, err := exec.LookPath("nmap"); err == nil && nmapPath != "" {
		// Discover active interfaces subnets to scan
		var subnets []string
		if ifaces, err := net.Interfaces(); err == nil {
			for _, iface := range ifaces {
				if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
					continue
				}
				if addrs, err := iface.Addrs(); err == nil {
					for _, addr := range addrs {
						if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() && ipNet.IP.To4() != nil {
							subnets = append(subnets, ipNet.String())
						}
					}
				}
			}
		}

		for _, sub := range subnets {
			nmapArgs := []string{"-p", "22", "--open", "-T4", "-n", "--min-rate", "400", sub}
			if nmapOut, err := exec.CommandContext(ctx, nmapPath, nmapArgs...).CombinedOutput(); err == nil {
				lines := strings.Split(string(nmapOut), "\n")
				for _, line := range lines {
					if strings.HasPrefix(line, "Nmap scan report for ") {
						targetIP := strings.TrimSpace(strings.TrimPrefix(line, "Nmap scan report for "))
						if net.ParseIP(targetIP) != nil {
							if _, exists := candMap[targetIP]; !exists {
								candMap[targetIP] = "nmap-scan"
							}
						}
					}
				}
			}
		}
	}

	// 7. Concurrently scan port 22 across all gathered candidates (1500ms dial timeout for WiFi latency tolerance, 250 workers)
	var (
		mu      sync.Mutex
		results []DiscoveredHost
		wg      sync.WaitGroup
		sem     = make(chan struct{}, 250)
	)

	for ip, devName := range candMap {
		wg.Add(1)
		go func(targetIP, dev string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			dialer := net.Dialer{Timeout: 1500 * time.Millisecond}
			conn, err := dialer.DialContext(ctx, "tcp", fmt.Sprintf("%s:22", targetIP))
			if err != nil {
				return
			}
			conn.Close()

			host := DiscoveredHost{
				IP:         targetIP,
				Port22Open: true,
				User:       defaultUser,
				Interface:  dev,
			}

			// Test passwordless SSH with defaultUser (workstation user e.g. fcurrie)
			sshCmd := exec.CommandContext(ctx, "ssh",
				"-o", "BatchMode=yes",
				"-o", "ConnectTimeout=3",
				"-o", "StrictHostKeyChecking=accept-new",
				fmt.Sprintf("%s@%s", defaultUser, targetIP),
				"hostname",
			)
			out, err := sshCmd.CombinedOutput()
			if err == nil {
				host.PasswordlessSSH = true
				host.Hostname = strings.TrimSpace(string(out))
			}

			mu.Lock()
			results = append(results, host)
			mu.Unlock()
		}(ip, devName)
	}

	wg.Wait()

	// Sort results by IP for clean output
	sort.Slice(results, func(i, j int) bool {
		return results[i].IP < results[j].IP
	})

	return results, nil
}

type TargetInput struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Host string `json:"host"`
	User string `json:"user"`
}

type TargetTelemetry struct {
	Name         string `json:"name"`
	Host         string `json:"host"`
	User         string `json:"user"`
	Type         string `json:"type"`
	Hostname     string `json:"hostname"`
	OS           string `json:"os"`
	Kernel       string `json:"kernel"`
	NetInterface string `json:"interface,omitempty"`
	Status       string `json:"status"`
}

func ProbeTargetTelemetry(ctx context.Context, t TargetInput, activeTarget string) TargetTelemetry {
	user := t.User
	if user == "" || user == "root" {
		user = GetDefaultSSHUser()
	}

	tt := TargetTelemetry{
		Name:   t.Name,
		Host:   t.Host,
		User:   user,
		Type:   t.Type,
		Status: "ONLINE",
	}

	if t.Name == activeTarget {
		tt.Status = "🎯 ACTIVE"
	}

	if t.Type == "local" || t.Host == "127.0.0.1" || t.Host == "localhost" || t.Host == "" {
		tt.Host = "127.0.0.1"
		tt.User = GetDefaultSSHUser()
		if h, err := os.Hostname(); err == nil {
			tt.Hostname = h
		} else {
			tt.Hostname = "localhost"
		}

		if data, err := os.ReadFile("/etc/os-release"); err == nil {
			lines := strings.Split(string(data), "\n")
			for _, line := range lines {
				if strings.HasPrefix(line, "PRETTY_NAME=") {
					pretty := strings.TrimPrefix(line, "PRETTY_NAME=")
					tt.OS = strings.Trim(pretty, "\"")
					break
				}
			}
		}
		if tt.OS == "" {
			tt.OS = fmt.Sprintf("%s/%s", "linux", "amd64")
		}

		cmd := exec.CommandContext(ctx, "uname", "-sr")
		if out, err := cmd.Output(); err == nil {
			tt.Kernel = strings.TrimSpace(string(out))
		} else {
			tt.Kernel = "Linux"
		}
		tt.NetInterface = "lo (local)"
		return tt
	}

	sshCmd := exec.CommandContext(ctx, "ssh",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=2",
		"-o", "StrictHostKeyChecking=accept-new",
		fmt.Sprintf("%s@%s", user, t.Host),
		"hostname; uname -sr; cat /etc/os-release 2>/dev/null | grep '^PRETTY_NAME=' | cut -d= -f2 | tr -d '\"'",
	)

	out, err := sshCmd.Output()
	if err != nil {
		tt.Status = "🔒 KEY NEEDED"
		tt.Hostname = "unknown"
		tt.OS = "unknown"
		tt.Kernel = "unknown"
		tt.NetInterface = "LAN / SSH"
		return tt
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) >= 1 && lines[0] != "" {
		tt.Hostname = strings.TrimSpace(lines[0])
	} else {
		tt.Hostname = "remote-host"
	}

	if len(lines) >= 2 && lines[1] != "" {
		tt.Kernel = strings.TrimSpace(lines[1])
	} else {
		tt.Kernel = "Linux"
	}

	if len(lines) >= 3 && lines[2] != "" {
		tt.OS = strings.TrimSpace(lines[2])
	} else {
		tt.OS = "Linux OS"
	}
	tt.NetInterface = "LAN / SSH"

	return tt
}

func ProbeAllTargets(ctx context.Context, targets []TargetInput, activeTarget string) []TargetTelemetry {
	results := make([]TargetTelemetry, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func(idx int, tc TargetInput) {
			defer wg.Done()
			results[idx] = ProbeTargetTelemetry(ctx, tc, activeTarget)
		}(i, t)
	}
	wg.Wait()
	return results
}

func FormatTargetsTable(telemetryList []TargetTelemetry, termWidth int) string {
	var sb strings.Builder
	sb.WriteString("🖥️ Monitored Target Systems & Remote Telemetry:\n\n")

	if termWidth <= 0 {
		termWidth = 100
	}

	// Account for TUI viewport margin
	effectiveWidth := termWidth - 4
	if effectiveWidth < 60 {
		effectiveWidth = 60
	}

	headers := []string{"TARGET / IP", "HOSTNAME", "OS RELEASE", "KERNEL", "SSH USER", "STATUS"}

	// Border & padding overhead: 7 vertical bars (│) + 12 spaces = 19 chars
	overhead := 19
	avail := effectiveWidth - overhead
	if avail < 41 {
		avail = 41
	}

	// Proportional column distribution: TARGET 18%, HOSTNAME 18%, OS 25%, KERNEL 21%, USER 8%, STATUS 10%
	wTarget := (avail * 18) / 100
	wHost := (avail * 18) / 100
	wOS := (avail * 25) / 100
	wKernel := (avail * 21) / 100
	wUser := (avail * 8) / 100
	wStatus := avail - (wTarget + wHost + wOS + wKernel + wUser)

	if wTarget < 11 {
		wTarget = 11
	}
	if wHost < 10 {
		wHost = 10
	}
	if wOS < 12 {
		wOS = 12
	}
	if wKernel < 12 {
		wKernel = 12
	}
	if wUser < 8 {
		wUser = 8
	}
	if wStatus < 10 {
		wStatus = 10
	}

	widths := []int{wTarget, wHost, wOS, wKernel, wUser, wStatus}

	pad := func(s string, w int) string {
		runes := []rune(s)
		if len(runes) > w {
			if w > 1 {
				return string(runes[:w-1]) + "…"
			}
			return string(runes[:w])
		}
		return s + strings.Repeat(" ", w-len(runes))
	}

	makeBorder := func(left, mid, right, fill string) string {
		var parts []string
		for _, w := range widths {
			parts = append(parts, strings.Repeat(fill, w+2))
		}
		return left + strings.Join(parts, mid) + right
	}

	sb.WriteString(makeBorder("┌", "┬", "┐", "─") + "\n│")
	for i, h := range headers {
		sb.WriteString(" " + pad(h, widths[i]) + " │")
	}
	sb.WriteString("\n" + makeBorder("├", "┼", "┤", "─") + "\n")

	for _, tt := range telemetryList {
		targetStr := tt.Name
		if tt.Host != "" && tt.Host != tt.Name && tt.Host != "127.0.0.1" {
			targetStr = fmt.Sprintf("%s (%s)", tt.Name, tt.Host)
		}

		sb.WriteString("│ ")
		sb.WriteString(pad(targetStr, widths[0]) + " │ ")
		sb.WriteString(pad(tt.Hostname, widths[1]) + " │ ")
		sb.WriteString(pad(tt.OS, widths[2]) + " │ ")
		sb.WriteString(pad(tt.Kernel, widths[3]) + " │ ")
		sb.WriteString(pad(tt.User, widths[4]) + " │ ")
		sb.WriteString(pad(tt.Status, widths[5]) + " │\n")
	}

	sb.WriteString(makeBorder("└", "┴", "┘", "─"))
	return sb.String()
}

func VerifyPasswordlessSSH(ctx context.Context, user, host string) (bool, string) {
	if host == "127.0.0.1" || host == "localhost" || host == "" {
		return true, "Localhost target (SSH check skipped)"
	}

	if user == "" || user == "root" {
		user = GetDefaultSSHUser()
	}

	sshCmd := exec.CommandContext(ctx, "ssh",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=3",
		"-o", "StrictHostKeyChecking=accept-new",
		fmt.Sprintf("%s@%s", user, host),
		"uptime",
	)
	out, err := sshCmd.CombinedOutput()
	if err == nil {
		return true, fmt.Sprintf("Connected successfully to %s@%s", user, host)
	}

	// Try default workstation user if specified user failed
	if defaultUser := GetDefaultSSHUser(); defaultUser != user {
		sshCmd2 := exec.CommandContext(ctx, "ssh",
			"-o", "BatchMode=yes",
			"-o", "ConnectTimeout=3",
			"-o", "StrictHostKeyChecking=accept-new",
			fmt.Sprintf("%s@%s", defaultUser, host),
			"uptime",
		)
		_, err2 := sshCmd2.CombinedOutput()
		if err2 == nil {
			return true, fmt.Sprintf("Connected successfully to %s@%s (Note: failed for %s)", defaultUser, host, user)
		}
	}

	outStr := strings.TrimSpace(string(out))
	if outStr == "" {
		outStr = "Connection timed out or host unreachable"
	}
	return false, fmt.Sprintf("Passwordless SSH check failed for %s@%s (%s).\nRun 'ssh-copy-id %s@%s' to authorize SSH key.", user, host, outStr, user, host)
}
