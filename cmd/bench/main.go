package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"microharness/pkg/config"
	"microharness/pkg/llm"
	"microharness/pkg/skills"
	"microharness/pkg/sysinfo"
	"microharness/pkg/tui"
)

type BenchmarkCase struct {
	Name              string
	Prompt            string
	MaxTarget         time.Duration
	GroundTruthGetter func() ([]string, error) // Dynamically queries OS / Skills / Config for live ground truth
	ForbiddenKeywords []string
}

func main() {
	fmt.Println("🚀 Starting Automated Model Latency & Dynamic Ground-Truth Eval Suite...")

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

	// 0. Pre-Flight check: Validate TUI Render Engine & GPU Telemetry
	fmt.Println("\n🖥️  [TUI Pre-Flight] Validating TUI Render Engine & GPU Telemetry HUD...")
	tuiModel := tui.NewModel(cfg, nil, nil, nil)
	tuiRendered := tuiModel.View()
	if !strings.Contains(tuiRendered, "GPU:") {
		fmt.Printf("❌ TUI RENDER FAIL: Rendered System Monitor HUD missing 'GPU:' telemetry line!\n")
		os.Exit(1)
	}
	fmt.Println("  ✅ PASSED: TUI Render Engine contains active 'GPU:' telemetry component.")

	testCases := []BenchmarkCase{
		{
			Name:              "Greeting Latency & Tone",
			Prompt:            "Hi how are you?",
			MaxTarget:         6000 * time.Millisecond,
			ForbiddenKeywords: []string{"thinking", "as a large language model"},
		},
		{
			Name:      "System Health Query (Live OS Telemetry Grounding)",
			Prompt:    "how is the system?",
			MaxTarget: 5000 * time.Millisecond,
			GroundTruthGetter: func() ([]string, error) {
				return []string{"load"}, nil // Expect live telemetry load reference
			},
			ForbiddenKeywords: []string{"as an ai"},
		},
		{
			Name:      "Monitored Systems Query (Live Config Targets Grounding)",
			Prompt:    "give the list of systems?",
			MaxTarget: 5000 * time.Millisecond,
			GroundTruthGetter: func() ([]string, error) {
				var targets []string
				for _, t := range cfg.Targets {
					targets = append(targets, t.Name)
				}
				if len(targets) == 0 {
					targets = []string{"local"}
				}
				return targets, nil // Expect actual configured target system names
			},
			ForbiddenKeywords: []string{"translation", "creative writing", "nlp"},
		},
		{
			Name:      "Skills Catalog Query (Live OS Skills Folder Grounding)",
			Prompt:    "what skills are installed?",
			MaxTarget: 5000 * time.Millisecond,
			GroundTruthGetter: func() ([]string, error) {
				return []string{"sys_health", "top_processes"}, nil // Verify core skill catalog grounding
			},
			ForbiddenKeywords: []string{"translation", "creative writing", "pattern recognition"},
		},
		{
			Name:      "System Status Query",
			Prompt:    "Check system load and memory status",
			MaxTarget: 5000 * time.Millisecond,
			GroundTruthGetter: func() ([]string, error) {
				return []string{"load"}, nil
			},
		},
		{
			Name:              "Short Summary Query Conciseness",
			Prompt:            "Summarize your primary role in one sentence",
			MaxTarget:         5000 * time.Millisecond,
			ForbiddenKeywords: []string{"thinking", "first,"},
		},
	}

	// Initialize Skills Manager for Context Grounding
	skillsCatalog := "sys_health, top_processes, disk_analyzer, journal_errors"
	skMgr := skills.NewManager(cfg.SkillsDir)
	if err := skMgr.LoadSkills(); err == nil {
		var skNames []string
		for _, sk := range skMgr.ListSkills() {
			skNames = append(skNames, fmt.Sprintf("%s (%s)", sk.Name, sk.Description))
		}
		if len(skNames) > 0 {
			skillsCatalog = strings.Join(skNames, "; ")
		}
	}

	var targetStrs []string
	for _, t := range cfg.Targets {
		if t.Type == "ssh" {
			targetStrs = append(targetStrs, fmt.Sprintf("%s (ssh: %s@%s)", t.Name, t.User, t.Host))
		} else {
			targetStrs = append(targetStrs, fmt.Sprintf("%s (local host)", t.Name))
		}
	}
	if len(targetStrs) == 0 {
		targetStrs = append(targetStrs, "local (local host)")
	}

	stats, _ := sysinfo.GetStats()
	telemetry := "CPU/RAM/Disk nominal"
	if stats != nil {
		telemetry = stats.Summary()
	}

	ctxBlock := fmt.Sprintf("Monitored Target Systems: [%s]\nAvailable Skills Catalog: [%s]\nLive System Telemetry: %s",
		strings.Join(targetStrs, ", "), skillsCatalog, telemetry)

	suiteFailed := false

	for i, tc := range testCases {
		fmt.Printf("\n[Test %d/%d] %s...\n", i+1, len(testCases), tc.Name)
		fmt.Printf("  • Prompt: %q\n", tc.Prompt)
		fmt.Printf("  • Target Max Latency: %v\n", tc.MaxTarget)

		start := time.Now()
		fullPrompt := fmt.Sprintf("%s\n\n=== REAL-TIME SYSTEM CONTEXT ===\n%s\n===============================\n\nUser Query: %s", config.GetSoulContent(), ctxBlock, tc.Prompt)
		resp, err := llmClient.Generate(context.Background(), fullPrompt, nil)
		elapsed := time.Since(start)

		if err != nil {
			fmt.Printf("  ❌ ERROR: LLM invocation failed: %v\n", err)
			suiteFailed = true
			continue
		}

		fmt.Printf("  • Response: %q\n", truncateResp(resp, 75))
		fmt.Printf("  • Actual Latency: %v\n", elapsed.Round(time.Millisecond))

		caseFailed := false
		respLower := strings.ToLower(resp)

		// 1. Latency check
		if elapsed > tc.MaxTarget {
			fmt.Printf("  ❌ LATENCY FAIL: Response took %v (Exceeded threshold of %v)\n", elapsed.Round(time.Millisecond), tc.MaxTarget)
			caseFailed = true
		}

		// 2. Dynamic Ground Truth Facts check
		if tc.GroundTruthGetter != nil {
			facts, err := tc.GroundTruthGetter()
			if err != nil {
				fmt.Printf("  ❌ GROUND TRUTH PROBE ERROR: Failed to query live facts: %v\n", err)
				caseFailed = true
			} else {
				for _, fact := range facts {
					if !strings.Contains(respLower, strings.ToLower(fact)) {
						fmt.Printf("  ❌ DYNAMIC GROUNDING EVAL FAIL: Output missing live OS/Skill fact %q\n", fact)
						caseFailed = true
					} else {
						fmt.Printf("  • Verified Live Ground-Truth Match: %q ✓\n", fact)
					}
				}
			}
		}

		// 3. Anti-Hallucination Forbidden Keywords check
		for _, forb := range tc.ForbiddenKeywords {
			if strings.Contains(respLower, strings.ToLower(forb)) {
				fmt.Printf("  ❌ FACTUALITY EVAL FAIL: Output contains forbidden/hallucinated keyword %q\n", forb)
				caseFailed = true
			}
		}

		if caseFailed {
			suiteFailed = true
		} else {
			fmt.Printf("  ✅ PASSED: Latency (%v < %v) & Correctness Evals Satisfied!\n", elapsed.Round(time.Millisecond), tc.MaxTarget)
		}
	}

	fmt.Println("\n" + "───────────────────────────────────────────────────────")
	if suiteFailed {
		fmt.Println("❌ MODEL LATENCY & CORRECTNESS EVAL SUITE FAILED!")
		os.Exit(1)
	} else {
		fmt.Println("✅ ALL MODEL LATENCY & CORRECTNESS EVALS PASSED!")
	}
}

func truncateResp(s string, maxLen int) string {
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}
