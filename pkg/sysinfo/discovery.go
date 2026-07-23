package sysinfo

import (
	"context"
	"fmt"
	"net"
	"os/exec"
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
}

func DiscoverNetworkTargets(ctx context.Context, defaultUser string) ([]DiscoveredHost, error) {
	if defaultUser == "" {
		defaultUser = "root"
	}

	ipMap := make(map[string]bool)

	// Candidate IP: PXE server
	ipMap["192.168.100.200"] = true

	// Parse ip neighbor / arp output
	out, err := exec.CommandContext(ctx, "ip", "neighbor").CombinedOutput()
	if err == nil {
		lines := strings.Split(string(out), "\n")
		for _, line := range lines {
			fields := strings.Fields(line)
			if len(fields) > 0 {
				ip := fields[0]
				if net.ParseIP(ip) != nil && !strings.HasPrefix(ip, "127.") {
					ipMap[ip] = true
				}
			}
		}
	}

	// Parse ip route
	routesOut, err := exec.CommandContext(ctx, "ip", "route").CombinedOutput()
	if err == nil {
		lines := strings.Split(string(routesOut), "\n")
		for _, line := range lines {
			fields := strings.Fields(line)
			for i, f := range fields {
				if f == "via" || f == "src" {
					if i+1 < len(fields) {
						ip := fields[i+1]
						if net.ParseIP(ip) != nil && !strings.HasPrefix(ip, "127.") {
							ipMap[ip] = true
						}
					}
				}
			}
		}
	}

	var (
		mu      sync.Mutex
		results []DiscoveredHost
		wg      sync.WaitGroup
	)

	for ip := range ipMap {
		wg.Add(1)
		go func(targetIP string) {
			defer wg.Done()

			dialer := net.Dialer{Timeout: 1 * time.Second}
			conn, err := dialer.DialContext(ctx, "tcp", fmt.Sprintf("%s:22", targetIP))
			if err != nil {
				return
			}
			conn.Close()

			host := DiscoveredHost{
				IP:         targetIP,
				Port22Open: true,
				User:       defaultUser,
			}

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
				if currentUser := "fcurrie"; currentUser != defaultUser {
					sshCmd2 := exec.CommandContext(ctx, "ssh",
						"-o", "BatchMode=yes",
						"-o", "ConnectTimeout=2",
						"-o", "StrictHostKeyChecking=accept-new",
						fmt.Sprintf("%s@%s", currentUser, targetIP),
						"hostname",
					)
					out2, err2 := sshCmd2.CombinedOutput()
					if err2 == nil {
						host.PasswordlessSSH = true
						host.User = currentUser
						host.Hostname = strings.TrimSpace(string(out2))
					}
				}
			}

			mu.Lock()
			results = append(results, host)
			mu.Unlock()
		}(ip)
	}

	wg.Wait()
	return results, nil
}

func VerifyPasswordlessSSH(ctx context.Context, user, host string) (bool, string) {
	if host == "127.0.0.1" || host == "localhost" || host == "" {
		return true, "Localhost target (SSH check skipped)"
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
	outStr := strings.TrimSpace(string(out))
	if outStr == "" {
		outStr = "Connection timed out or host unreachable"
	}
	return false, fmt.Sprintf("Passwordless SSH failed (%s). Run 'ssh-copy-id %s@%s' to authorize key.", outStr, user, host)
}
