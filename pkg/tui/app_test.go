package tui

import (
	"testing"
)

func TestParseAddTargetInput(t *testing.T) {
	tests := []struct {
		input        string
		expectedName string
		expectedHost string
		expectedUser string
		expectedOk   bool
	}{
		{
			input:        "add host 192.168.100.200",
			expectedName: "192.168.100.200",
			expectedHost: "192.168.100.200",
			expectedUser: "fcurrie",
			expectedOk:   true,
		},
		{
			input:        "add host fcurrie@192.168.100.200",
			expectedName: "192.168.100.200",
			expectedHost: "192.168.100.200",
			expectedUser: "fcurrie",
			expectedOk:   true,
		},
		{
			input:        "add target pxe-server 192.168.100.200 fcurrie",
			expectedName: "pxe-server",
			expectedHost: "192.168.100.200",
			expectedUser: "fcurrie",
			expectedOk:   true,
		},
		{
			input:        "add target pxe-server | 192.168.100.200 | fcurrie",
			expectedName: "pxe-server",
			expectedHost: "192.168.100.200",
			expectedUser: "fcurrie",
			expectedOk:   true,
		},
		{
			input:        "add host 192.168.100.200 fcurrie",
			expectedName: "192.168.100.200",
			expectedHost: "192.168.100.200",
			expectedUser: "fcurrie",
			expectedOk:   true,
		},
	}

	for _, tt := range tests {
		name, host, user, ok := ParseAddTargetInput(tt.input)
		if ok != tt.expectedOk {
			t.Errorf("ParseAddTargetInput(%q) ok = %v; want %v", tt.input, ok, tt.expectedOk)
		}
		if name != tt.expectedName {
			t.Errorf("ParseAddTargetInput(%q) name = %q; want %q", tt.input, name, tt.expectedName)
		}
		if host != tt.expectedHost {
			t.Errorf("ParseAddTargetInput(%q) host = %q; want %q", tt.input, host, tt.expectedHost)
		}
		if user != tt.expectedUser {
			t.Errorf("ParseAddTargetInput(%q) user = %q; want %q", tt.input, user, tt.expectedUser)
		}
	}
}
