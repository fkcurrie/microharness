package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"microharness/pkg/config"
	"microharness/pkg/llm"
)

type BenchmarkCase struct {
	Name      string
	Prompt    string
	MaxTarget time.Duration
}

func main() {
	fmt.Println("🚀 Starting Automated Model Latency & Performance Regression Suite...")

	home, _ := os.UserHomeDir()
	configPath := filepath.Join(home, ".config", "microharness", "config.yaml")

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		fmt.Printf("❌ Failed to load config: %v\n", err)
		os.Exit(1)
	}

	llmClient, err := llm.NewClient(&cfg.LLM)
	if err != nil {
		fmt.Printf("❌ Failed to initialize LLM client: %v\n", err)
		os.Exit(1)
	}

	testCases := []BenchmarkCase{
		{
			Name:      "Greeting Latency",
			Prompt:    "Hi how are you?",
			MaxTarget: 5000 * time.Millisecond,
		},
		{
			Name:      "System Status Query",
			Prompt:    "Check system load and memory status",
			MaxTarget: 5000 * time.Millisecond,
		},
		{
			Name:      "Short Summary Query",
			Prompt:    "Summarize your primary role in one sentence",
			MaxTarget: 5000 * time.Millisecond,
		},
	}

	failed := false

	for i, tc := range testCases {
		fmt.Printf("\n[Test %d/%d] %s...\n", i+1, len(testCases), tc.Name)
		fmt.Printf("  • Prompt: %q\n", tc.Prompt)
		fmt.Printf("  • Target Max Latency: %v\n", tc.MaxTarget)

		start := time.Now()
		resp, err := llmClient.Generate(context.Background(), tc.Prompt, nil)
		elapsed := time.Since(start)

		if err != nil {
			fmt.Printf("  ❌ ERROR: LLM invocation failed: %v\n", err)
			failed = true
			continue
		}

		fmt.Printf("  • Response: %q\n", truncateResp(resp, 70))
		fmt.Printf("  • Actual Latency: %v\n", elapsed.Round(time.Millisecond))

		if elapsed > tc.MaxTarget {
			fmt.Printf("  ❌ REGRESSION DETECTED: Response took %v (Exceeded threshold of %v)\n", elapsed.Round(time.Millisecond), tc.MaxTarget)
			failed = true
		} else {
			fmt.Printf("  ✅ PASSED: Response latency within acceptable bounds (%v < %v)\n", elapsed.Round(time.Millisecond), tc.MaxTarget)
		}
	}

	fmt.Println("\n" + "───────────────────────────────────────────────────────")
	if failed {
		fmt.Println("❌ LATENCY REGRESSION SUITE FAILED!")
		os.Exit(1)
	} else {
		fmt.Println("✅ ALL MODEL LATENCY & PERFORMANCE TESTS PASSED!")
	}
}

func truncateResp(s string, maxLen int) string {
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}
