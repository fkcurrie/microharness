package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

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

	case "set-provider":
		if len(args) < 2 {
			fmt.Println("Usage: microharness set-provider <gemini|claude|ollama>")
			os.Exit(1)
		}
		runSetProvider(*configPath, args[1])

	case "model":
		if len(args) < 2 {
			fmt.Println("Usage:\n  microharness model list\n  microharness model pull <model_name>")
			os.Exit(1)
		}
		subCmd := args[1]
		if subCmd == "list" {
			runModelList(*configPath)
		} else if subCmd == "pull" && len(args) >= 3 {
			runModelPull(*configPath, args[2])
		} else {
			fmt.Println("Usage:\n  microharness model list\n  microharness model pull <model_name>")
		}

	default:
		runTUI(*configPath)
	}
}

func runSetProvider(configPath, provider string) {
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	cfg.LLM.DefaultProvider = provider
	if err := cfg.Save(configPath); err != nil {
		log.Fatalf("Failed to save config: %v", err)
	}

	fmt.Printf("✅ Active LLM provider updated to: '%s'\n", provider)
}

func runModelList(configPath string) {
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		cfg = config.DefaultConfig()
	}

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(cfg.LLM.Ollama.Endpoint + "/api/tags")
	if err != nil {
		fmt.Printf("❌ Unable to connect to local Ollama server at %s: %v\n", cfg.LLM.Ollama.Endpoint, err)
		return
	}
	defer resp.Body.Close()

	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		fmt.Printf("Error parsing response: %v\n", err)
		return
	}

	fmt.Println("=== Installed Local Open Models ===")
	if len(tags.Models) == 0 {
		fmt.Println("  (No models installed yet. Run 'microharness model pull gemma4:e2b' to download one)")
		return
	}
	for _, m := range tags.Models {
		fmt.Printf("  • %s\n", m.Name)
	}
}

func runModelPull(configPath, modelName string) {
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		cfg = config.DefaultConfig()
	}

	fmt.Printf("⏳ Downloading pre-trained model '%s' via Ollama (%s)...\n", modelName, cfg.LLM.Ollama.Endpoint)

	body, _ := json.Marshal(map[string]interface{}{
		"name":   modelName,
		"stream": false,
	})

	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Post(cfg.LLM.Ollama.Endpoint+"/api/pull", "application/json", bytes.NewBuffer(body))
	if err != nil {
		log.Fatalf("❌ Download failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Fatalf("❌ Ollama API returned status code %d", resp.StatusCode)
	}

	// Update config to use this model and provider
	cfg.LLM.Ollama.Model = modelName
	cfg.LLM.DefaultProvider = "ollama"
	_ = cfg.Save(configPath)

	fmt.Printf("✅ Pre-trained model '%s' downloaded successfully!\n", modelName)
	fmt.Printf("   Updated config: default_provider set to 'ollama' (model: %s)\n", modelName)
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

	llmClient, err := llm.NewClient(&cfg.LLM)
	if err != nil {
		log.Printf("LLM Initialization Notice: %v (falling back to offline mode)", err)
	}

	model := tui.NewModel(cfg, llmClient, skillMgr, dbStore)
	p := tea.NewProgram(model, tea.WithAltScreen())

	if _, err := p.Run(); err != nil {
		fmt.Printf("Error running TUI: %v\n", err)
		os.Exit(1)
	}
}
