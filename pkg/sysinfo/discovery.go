package sysinfo

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
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

func DiscoverNetworkTargets(ctx context.Context, defaultUser string) ([]DiscoveredHost, error) {
	if defaultUser == "" || defaultUser == "root" {
		defaultUser = GetDefaultSSHUser()
	}

	type candidate struct {
		ip    string
		iface string
	}

	candMap := make(map[string]string) // ip -> interface

	// Candidate IP: Known PXE server
	candMap["192.168.100.200"] = "pxe-net"

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

				// Generate host candidate IPs for subnets <= /24
				ones, bits := ipNet.Mask.Size()
				if ones >= 24 && bits == 32 {
					baseIP := ipNet.IP.To4()
					for i := 1; i <= 254; i++ {
						ip := net.IPv4(baseIP[0], baseIP[1], baseIP[2], byte(i)).String()
						if ip != baseIP.String() {
							if _, exists := candMap[ip]; !exists {
								candMap[ip] = ifaceLabel
							}
						}
					}
				}
			}
		}
	}

	// 2. Parse ARP / ip neighbor table across all interfaces
	out, err := exec.CommandContext(ctx, "ip", "neighbor").CombinedOutput()
	if err == nil {
		lines := strings.Split(string(out), "\n")
		for _, line := range lines {
			fields := strings.Fields(line)
			if len(fields) >= 3 {
				ip := fields[0]
				if net.ParseIP(ip) != nil && !strings.HasPrefix(ip, "127.") {
					dev := "lan"
					for i, f := range fields {
						if f == "dev" && i+1 < len(fields) {
							dev = fields[i+1]
						}
					}
					candMap[ip] = dev
				}
			}
		}
	}

	// 3. Concurrently dial port 22 across all gathered candidates
	var (
		mu      sync.Mutex
		results []DiscoveredHost
		wg      sync.WaitGroup
		sem     = make(chan struct{}, 100)
	)

	for ip, devName := range candMap {
		wg.Add(1)
		go func(targetIP, dev string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			dialer := net.Dialer{Timeout: 500 * time.Millisecond}
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

			// Test passwordless SSH with defaultUser (workstation user)
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
					out2, err2 := sshCmd2.CombinedOutput()
					if err2 == nil {
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
	return results, nil
}

func VerifyPasswordlessSSH(ctx context.Context, user, host string) (bool, string) {
	if host == "127.0.0.1" || host == "localhost" || host == "" {
		return true, "Localhost target (SSH check skipped)"
	}

	if user == "" || user == "root" {
		// Default to workstation user if user wasn't explicitly supplied as non-root
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
