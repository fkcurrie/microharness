package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"microharness/pkg/config"
	"microharness/pkg/llm"
	"microharness/pkg/scheduler"
	"microharness/pkg/skills"
	"microharness/pkg/store"
	"microharness/pkg/tui"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	home, _ := os.UserHomeDir()
	defaultConfigPath := filepath.Join(home, ".config", "microharness", "config.yaml")

	configPath := flag.String("config", defaultConfigPath, "Path to config file")
	flag.Parse()

	args := flag.Args()
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}

	switch cmd {
	case "init":
		runInit(*configPath)

	case "daemon":
		runDaemon(*configPath)

	default:
		runTUI(*configPath)
	}
}

func runInit(configPath string) {
	fmt.Println("🔍 Running MicroHarness Auto-Discovery Wizard...")
	cfg, discovered := config.AutoDiscover()

	fmt.Println("\n=== Discovered System Capabilities ===")
	for k, v := range discovered {
		fmt.Printf("  • %-22s : %s\n", k, v)
	}

	if err := cfg.Save(configPath); err != nil {
		log.Fatalf("❌ Failed to save config to %s: %v", configPath, err)
	}

	// Create default skills directory and files
	_ = os.MkdirAll(cfg.SkillsDir, 0755)

	fmt.Printf("\n✅ MicroHarness initialized successfully!\n")
	fmt.Printf("   Config file saved to: %s\n", configPath)
	fmt.Printf("   Skills directory:     %s\n", cfg.SkillsDir)
	fmt.Printf("   SQLite Database:      %s\n\n", cfg.DBPath)
	fmt.Println("Type 'microharness' to launch the ASCII TUI Interface.")
}

func runDaemon(configPath string) {
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		log.Fatalf("Failed to load config from %s: %v (run 'microharness init' first)", configPath, err)
	}

	dbStore, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("Failed to open SQLite database: %v", err)
	}
	defer dbStore.Close()

	sched := scheduler.New(cfg.Jobs, dbStore)
	sched.Start()
	defer sched.Stop()

	log.Printf("🚀 MicroHarness Daemon running... Press Ctrl+C to exit.")
	select {}
}

func runTUI(configPath string) {
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		// Auto-initialize if config file does not exist!
		cfg, _ = config.AutoDiscover()
		_ = cfg.Save(configPath)
	}

	dbStore, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Printf("Warning: Unable to open SQLite database: %v", err)
	} else {
		defer dbStore.Close()
	}

	skillMgr := skills.NewManager(cfg.SkillsDir)
	_ = skillMgr.LoadSkills()

	// Initialize LLM Client
	llmClient, err := llm.NewClient(&cfg.LLM)
	if err != nil {
		log.Printf("LLM Initialization Notice: %v (falling back to offline mode)", err)
	}

	// Also ensure local skills directory exists
	localSkillMgr := skills.NewManager("skills")
	_ = localSkillMgr.LoadSkills()
	for _, s := range localSkillMgr.ListSkills() {
		_ = s
	}

	model := tui.NewModel(cfg, llmClient, skillMgr, dbStore)
	p := tea.NewProgram(model, tea.WithAltScreen())

	if _, err := p.Run(); err != nil {
		fmt.Printf("Error running TUI: %v\n", err)
		os.Exit(1)
	}
}
