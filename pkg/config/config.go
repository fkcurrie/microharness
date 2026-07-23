package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server    ServerConfig   `yaml:"server"`
	LLM       LLMConfig      `yaml:"llm"`
	Targets   []TargetConfig `yaml:"targets"`
	Jobs      []JobConfig    `yaml:"jobs"`
	SkillsDir string         `yaml:"skills_dir"`
	DBPath    string         `yaml:"db_path"`
}

type ServerConfig struct {
	SocketPath string `yaml:"socket_path"`
}

type LLMConfig struct {
	DefaultProvider string       `yaml:"default_provider"` // "gemini", "claude", "ollama"
	Gemini          GeminiConfig `yaml:"gemini"`
	Claude          ClaudeConfig `yaml:"claude"`
	Ollama          OllamaConfig `yaml:"ollama"`
}

type GeminiConfig struct {
	APIKey string `yaml:"api_key"`
	Model  string `yaml:"model"`
}

type ClaudeConfig struct {
	APIKey string `yaml:"api_key"`
	Model  string `yaml:"model"`
}

type OllamaConfig struct {
	Endpoint string `yaml:"endpoint"`
	Model    string `yaml:"model"`
}

type TargetConfig struct {
	Name    string `yaml:"name"`
	Type    string `yaml:"type"` // "local", "ssh"
	Host    string `yaml:"host,omitempty"`
	User    string `yaml:"user,omitempty"`
	KeyPath string `yaml:"key_path,omitempty"`
}

type JobConfig struct {
	Name     string `yaml:"name"`
	Schedule string `yaml:"schedule"` // e.g., "@every 10m"
	Command  string `yaml:"command"`
	Target   string `yaml:"target"`
	Enabled  bool   `yaml:"enabled"`
}

func DefaultConfig() *Config {
	home, _ := os.UserHomeDir()
	baseDir := filepath.Join(home, ".config", "microharness")

	return &Config{
		Server: ServerConfig{
			SocketPath: filepath.Join(baseDir, "microharness.sock"),
		},
		LLM: LLMConfig{
			DefaultProvider: "gemini",
			Gemini: GeminiConfig{
				APIKey: os.Getenv("GEMINI_API_KEY"),
				Model:  "gemini-2.5-flash",
			},
			Claude: ClaudeConfig{
				APIKey: os.Getenv("ANTHROPIC_API_KEY"),
				Model:  "claude-3-5-sonnet-latest",
			},
			Ollama: OllamaConfig{
				Endpoint: "http://127.0.0.1:11434",
				Model:    "gemma2:2b",
			},
		},
		Targets: []TargetConfig{
			{
				Name: "local",
				Type: "local",
			},
		},
		Jobs: []JobConfig{
			{
				Name:     "sys_health_check",
				Schedule: "@every 15m",
				Command:  filepath.Join(baseDir, "skills", "sys_health.sh"),
				Target:   "local",
				Enabled:  true,
			},
			{
				Name:     "top_processes_check",
				Schedule: "@every 5m",
				Command:  filepath.Join(baseDir, "skills", "top_processes.sh"),
				Target:   "local",
				Enabled:  true,
			},
			{
				Name:     "journal_errors_check",
				Schedule: "@every 30m",
				Command:  filepath.Join(baseDir, "skills", "journal_errors.sh") + " 1h",
				Target:   "local",
				Enabled:  true,
			},
			{
				Name:     "disk_analyzer_check",
				Schedule: "@every 1h",
				Command:  filepath.Join(baseDir, "skills", "disk_analyzer.sh"),
				Target:   "local",
				Enabled:  true,
			},
		},
		SkillsDir: filepath.Join(baseDir, "skills"),
		DBPath:    filepath.Join(baseDir, "harness.db"),
	}
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	cfg := DefaultConfig()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

func (c *Config) Save(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}
