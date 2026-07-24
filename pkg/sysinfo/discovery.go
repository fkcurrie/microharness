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

	// 6. Concurrently scan port 22 across all gathered candidates (300ms dial timeout, 200 workers)
	var (
		mu      sync.Mutex
		results []DiscoveredHost
		wg      sync.WaitGroup
		sem     = make(chan struct{}, 200)
	)

	for ip, devName := range candMap {
		wg.Add(1)
		go func(targetIP, dev string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			dialer := net.Dialer{Timeout: 300 * time.Millisecond}
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
				"-o", "ConnectTimeout=2",
				"-o", "StrictHostKeyChecking=accept-new",
				fmt.Sprintf("%s@%s", defaultUser, targetIP),
				"hostname",
			)
			out, err := sshCmd.CombinedOutput()
			if err == nil {
				host.PasswordlessSSH = true
				host.Hostname = strings.TrimSpace(string(out))
			} else {
				// Fallback test with 'root' if defaultUser was different
				if defaultUser != "root" {
					sshCmd2 := exec.CommandContext(ctx, "ssh",
						"-o", "BatchMode=yes",
						"-o", "ConnectTimeout=2",
						"-o", "StrictHostKeyChecking=accept-new",
						fmt.Sprintf("root@%s", targetIP),
						"hostname",
					)
					if out2, err2 := sshCmd2.CombinedOutput(); err2 == nil {
						host.PasswordlessSSH = true
						host.User = "root"
						host.Hostname = strings.TrimSpace(string(out2))
					}
				}
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
