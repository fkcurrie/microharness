package skills

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Skill struct {
	Name        string                 `yaml:"name" json:"name"`
	Description string                 `yaml:"description" json:"description"`
	Script      string                 `yaml:"script" json:"script"`
	Parameters  map[string]interface{} `yaml:"parameters" json:"parameters"`
	Dir         string                 `json:"-"`
}

type Manager struct {
	skillsDir string
	skills    map[string]*Skill
}

func NewManager(skillsDir string) *Manager {
	return &Manager{
		skillsDir: skillsDir,
		skills:    make(map[string]*Skill),
	}
}

func (m *Manager) LoadSkills() error {
	if _, err := os.Stat(m.skillsDir); os.IsNotExist(err) {
		return nil
	}

	entries, err := os.ReadDir(m.skillsDir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".yaml" {
			path := filepath.Join(m.skillsDir, entry.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}

			var s Skill
			if err := yaml.Unmarshal(data, &s); err == nil && s.Name != "" {
				s.Dir = m.skillsDir
				m.skills[s.Name] = &s
			}
		}
	}
	return nil
}

func (m *Manager) ListSkills() []*Skill {
	var list []*Skill
	for _, s := range m.skills {
		list = append(list, s)
	}
	return list
}

func (m *Manager) Execute(ctx context.Context, name string, args []string) (string, error) {
	skill, ok := m.skills[name]
	if !ok {
		return "", fmt.Errorf("skill '%s' not found", name)
	}

	scriptPath := filepath.Join(skill.Dir, skill.Script)
	if _, err := os.Stat(scriptPath); err != nil {
		return "", fmt.Errorf("skill script '%s' not found at %s", skill.Script, scriptPath)
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, scriptPath, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("skill execution error: %w | output: %s", err, string(output))
	}

	return string(output), nil
}

func (m *Manager) ExecuteOnTarget(ctx context.Context, name string, user, host string, args []string) (string, error) {
	if host == "127.0.0.1" || host == "localhost" || host == "" || host == "local" {
		return m.Execute(ctx, name, args)
	}

	skill, ok := m.skills[name]
	if !ok {
		return "", fmt.Errorf("skill '%s' not found", name)
	}

	scriptPath := filepath.Join(skill.Dir, skill.Script)
	scriptBytes, err := os.ReadFile(scriptPath)
	if err != nil {
		return "", fmt.Errorf("failed to read skill script at %s: %w", scriptPath, err)
	}

	if user == "" {
		user = "root"
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	sshCmd := exec.CommandContext(ctx, "ssh",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=5",
		"-o", "StrictHostKeyChecking=accept-new",
		fmt.Sprintf("%s@%s", user, host),
		"bash -s",
	)
	sshCmd.Stdin = strings.NewReader(string(scriptBytes))
	output, err := sshCmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("remote SSH skill execution error on %s@%s: %w | output: %s", user, host, err, string(output))
	}

	return string(output), nil
}

func (m *Manager) CreateSkill(name, description, scriptContent string) (*Skill, error) {
	if err := os.MkdirAll(m.skillsDir, 0755); err != nil {
		return nil, err
	}

	scriptName := fmt.Sprintf("%s.sh", name)
	scriptPath := filepath.Join(m.skillsDir, scriptName)
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
		return nil, fmt.Errorf("failed to write skill script: %w", err)
	}

	skill := Skill{
		Name:        name,
		Description: description,
		Script:      scriptName,
		Parameters: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
		Dir: m.skillsDir,
	}

	yamlData, err := yaml.Marshal(skill)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal skill spec: %w", err)
	}

	yamlPath := filepath.Join(m.skillsDir, fmt.Sprintf("%s.yaml", name))
	if err := os.WriteFile(yamlPath, yamlData, 0644); err != nil {
		return nil, fmt.Errorf("failed to write skill yaml: %w", err)
	}

	m.skills[name] = &skill
	return &skill, nil
}
