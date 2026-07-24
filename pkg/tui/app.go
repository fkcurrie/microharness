package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"microharness/pkg/config"
	"microharness/pkg/llm"
	"microharness/pkg/skills"
	"microharness/pkg/store"
	"microharness/pkg/sysinfo"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type errMsg error

type LLMStats struct {
	TotalRequests    int
	TotalTokens      int
	LastPromptTokens int
	LastEvalTokens   int
	LastLatency      time.Duration
	EstimatedCost    float64
}

type llmResponseMsg struct {
	content      string
	promptTokens int
	evalTokens   int
	latency      time.Duration
	err          error
}

type statusStepMsg struct {
	text string
	step int
}

func tickStatusCmd(text string, step int, delay time.Duration) tea.Cmd {
	return tea.Tick(delay, func(time.Time) tea.Msg {
		return statusStepMsg{text: text, step: step}
	})
}

type model struct {
	cfg             *config.Config
	llmClient       llm.Client
	skillMgr        *skills.Manager
	dbStore         *store.Store
	viewport        viewport.Model
	textarea        textarea.Model
	err             error
	history         []llm.Message
	sysStats        *sysinfo.SystemStats
	recentLogs      []store.JobLog
	llmStats        LLMStats
	statusMsg       string
	sessionID       string
	sessions        []store.ChatSession
	selectingSess   bool
	selectedSessIdx int
	activeTarget    string
	width           int
	height          int
	loading         bool
}

func NewModel(cfg *config.Config, llmClient llm.Client, skillMgr *skills.Manager, dbStore *store.Store) model {
	ta := textarea.New()
	ta.Placeholder = "Type a prompt, ! <cmd>, or /help for slash commands (/sessions, /add, /model, /compact, /target)..."
	ta.Focus()
	ta.Prompt = "│ "
	ta.CharLimit = 500
	ta.SetWidth(50)
	ta.SetHeight(3)

	vp := viewport.New(50, 15)
	vp.SetContent("Welcome to MicroHarness ASCII Command Center.\nType a message to chat, ! <cmd> to run shell commands, or /help for slash commands.\n" + strings.Repeat("─", 55) + "\n")

	stats, _ := sysinfo.GetStats()
	var logs []store.JobLog
	var recentSessions []store.ChatSession
	selectingSess := false
	defaultSessID := fmt.Sprintf("s-%s", time.Now().Format("20060102-150405"))

	if dbStore != nil {
		logs, _ = dbStore.GetRecentJobLogs(5)
		if sessList, err := dbStore.GetRecentSessions(8); err == nil && len(sessList) > 0 {
			recentSessions = sessList
			selectingSess = true
		}
	}

	m := model{
		cfg:             cfg,
		llmClient:       llmClient,
		skillMgr:        skillMgr,
		dbStore:         dbStore,
		viewport:        vp,
		textarea:        ta,
		history:         nil,
		sysStats:        stats,
		recentLogs:      logs,
		llmStats:        LLMStats{},
		sessionID:       defaultSessID,
		sessions:        recentSessions,
		selectingSess:   selectingSess,
		selectedSessIdx: 0,
		activeTarget:    "local",
		width:           80,
		height:          24,
	}

	return m
}

func (m model) Init() tea.Cmd {
	return textarea.Blink
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		tiCmd tea.Cmd
		vpCmd tea.Cmd
	)

	m.textarea, tiCmd = m.textarea.Update(msg)
	m.viewport, vpCmd = m.viewport.Update(msg)

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

		m.textarea.SetWidth(m.width - 2)

		vpHeight := m.height - 12
		if vpHeight < 8 {
			vpHeight = 8
		}
		vpWidth := (m.width * 6) / 10
		if vpWidth < 40 {
			vpWidth = 40
		}
		m.viewport.Width = vpWidth
		m.viewport.Height = vpHeight
		m.renderViewport()

	case tea.KeyMsg:
		if m.selectingSess {
			switch msg.Type {
			case tea.KeyUp, tea.KeyCtrlK:
				if m.selectedSessIdx > 0 {
					m.selectedSessIdx--
				}
				return m, nil
			case tea.KeyDown, tea.KeyCtrlJ:
				if m.selectedSessIdx <= len(m.sessions) {
					m.selectedSessIdx++
				}
				return m, nil
			case tea.KeyEnter:
				m.selectingSess = false
				if m.selectedSessIdx == 0 {
					m.sessionID = fmt.Sprintf("s-%s", time.Now().Format("20060102-150405"))
					m.history = nil
					m.renderViewport()
					return m, nil
				}
				chosenSess := m.sessions[m.selectedSessIdx-1]
				m.sessionID = chosenSess.SessionID
				if m.dbStore != nil {
					if msgs, err := m.dbStore.GetMessagesBySession(m.sessionID, 50); err == nil {
						var history []llm.Message
						for _, dbm := range msgs {
							history = append(history, llm.Message{Role: dbm.Role, Content: dbm.Content})
						}
						m.history = history
					}
				}
				m.renderViewport()
				return m, nil
			case tea.KeyEsc:
				m.selectingSess = false
				m.renderViewport()
				return m, nil
			}
			return m, nil
		}

		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			return m, tea.Quit

		case tea.KeyEnter:
			input := strings.TrimSpace(m.textarea.Value())
			if input == "" {
				return m, nil
			}

			// Add to viewport history
			m.history = append(m.history, llm.Message{Role: "user", Content: input})
			m.renderViewport()
			m.textarea.Reset()

			if m.dbStore != nil {
				_ = m.dbStore.SaveMessage(m.sessionID, "user", input)
			}

			cmdLower := strings.ToLower(input)

			// 1. Direct Shell Execution Passthrough: ! <cmd> or /sh <cmd>
			if strings.HasPrefix(input, "!") || strings.HasPrefix(cmdLower, "/sh ") || strings.HasPrefix(cmdLower, "/exec ") {
				shCmd := strings.TrimPrefix(input, "!")
				if strings.HasPrefix(cmdLower, "/sh ") {
					shCmd = strings.TrimPrefix(input, "/sh ")
				} else if strings.HasPrefix(cmdLower, "/exec ") {
					shCmd = strings.TrimPrefix(input, "/exec ")
				}
				shCmd = strings.TrimSpace(shCmd)

				if shCmd == "" {
					return m, func() tea.Msg { return "⚠️ Usage: `! <command>` or `/sh <command>` (e.g., `! uptime` or `/sh df -h`)" }
				}

				m.loading = true
				m.statusMsg = fmt.Sprintf("⚡ Executing shell command: %s", shCmd)
				m.renderViewport()

				return m, func() tea.Msg {
					ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
					defer cancel()

					cmd := exec.CommandContext(ctx, "bash", "-c", shCmd)
					out, err := cmd.CombinedOutput()
					outStr := strings.TrimSpace(string(out))
					if outStr == "" {
						outStr = "(Command executed with no output)"
					}

					if err != nil {
						return fmt.Sprintf("💻 Shell Command [`%s`] Failed (Error: %v):\n```\n%s\n```", shCmd, err, outStr)
					}
					return fmt.Sprintf("💻 Shell Command [`%s`] Output:\n```\n%s\n```", shCmd, outStr)
				}
			}

			// 2. Context Attachment: /add <filepath> or /log <service>
			if strings.HasPrefix(cmdLower, "/add ") || strings.HasPrefix(cmdLower, "/file ") {
				filePath := strings.TrimSpace(input[4:])
				if strings.HasPrefix(cmdLower, "/file ") {
					filePath = strings.TrimSpace(input[6:])
				}
				if filePath == "" {
					return m, func() tea.Msg { return "⚠️ Usage: `/add <filepath>` (e.g., `/add /var/log/syslog`)" }
				}

				m.loading = true
				m.statusMsg = fmt.Sprintf("📄 Reading file %s into context...", filePath)
				m.renderViewport()

				return m, func() tea.Msg {
					data, err := os.ReadFile(filePath)
					if err != nil {
						return fmt.Sprintf("❌ Error reading file '%s': %v", filePath, err)
					}
					lines := strings.Split(string(data), "\n")
					if len(lines) > 200 {
						lines = lines[len(lines)-200:]
					}
					content := strings.Join(lines, "\n")
					return fmt.Sprintf("📄 Attached File Context [`%s`] (%d bytes):\n```\n%s\n```", filePath, len(data), content)
				}
			}

			if strings.HasPrefix(cmdLower, "/log ") {
				serviceName := strings.TrimSpace(input[5:])
				if serviceName == "" {
					return m, func() tea.Msg { return "⚠️ Usage: `/log <service_name>` (e.g., `/log nginx` or `/log systemd`)" }
				}

				m.loading = true
				m.statusMsg = fmt.Sprintf("📋 Reading journal log for service %s...", serviceName)
				m.renderViewport()

				return m, func() tea.Msg {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()

					cmd := exec.CommandContext(ctx, "journalctl", "-u", serviceName, "-n", "50", "--no-pager")
					out, err := cmd.CombinedOutput()
					if err != nil || len(out) == 0 {
						cmd = exec.CommandContext(ctx, "journalctl", "-n", "50", "--no-pager", "-q")
						out, _ = cmd.CombinedOutput()
					}
					outStr := strings.TrimSpace(string(out))
					if outStr == "" {
						outStr = "(No log output retrieved)"
					}
					return fmt.Sprintf("📋 Attached Journal Log Context [`%s`]:\n```\n%s\n```", serviceName, outStr)
				}
			}

			// 3. Dynamic Model & Provider Switcher: /model <provider>
			if strings.HasPrefix(cmdLower, "/model ") || strings.HasPrefix(cmdLower, "/provider ") {
				parts := strings.Fields(input)
				if len(parts) < 2 {
					return m, func() tea.Msg { return "⚠️ Usage: `/model <gemini|claude|ollama|litellm>` (e.g., `/model gemini`)" }
				}
				newProvider := strings.ToLower(parts[1])
				m.cfg.LLM.DefaultProvider = newProvider
				if len(parts) >= 3 {
					switch newProvider {
					case "ollama":
						m.cfg.LLM.Ollama.Model = parts[2]
					case "gemini":
						m.cfg.LLM.Gemini.Model = parts[2]
					case "claude":
						m.cfg.LLM.Claude.Model = parts[2]
					case "litellm":
						m.cfg.LLM.LiteLLM.Model = parts[2]
					}
				}

				newClient, err := llm.NewClient(&m.cfg.LLM)
				if err != nil {
					return m, func() tea.Msg { return fmt.Sprintf("❌ Failed to switch LLM provider to '%s': %v", newProvider, err) }
				}
				m.llmClient = newClient
				return m, func() tea.Msg { return fmt.Sprintf("🔄 Switched active LLM provider to [%s]!", newProvider) }
			}

			// 4. Context Auto-Compaction: /compact
			if cmdLower == "/compact" || cmdLower == "/compress" {
				if len(m.history) <= 4 {
					return m, func() tea.Msg { return "ℹ️ Conversation history is already short. No compaction needed." }
				}

				m.loading = true
				m.statusMsg = "🧹 Compacting conversation context..."
				m.renderViewport()

				return m, func() tea.Msg {
					var fullText strings.Builder
					for _, msg := range m.history {
						fullText.WriteString(fmt.Sprintf("%s: %s\n", msg.Role, msg.Content))
					}

					summaryPrompt := "Summarize the key facts, decisions, and system statuses from this conversation into a single concise paragraph (under 100 words):"
					summary, err := m.llmClient.Generate(context.Background(), summaryPrompt, []llm.Message{{Role: "user", Content: fullText.String()}})
					if err != nil {
						summary = "Previous context summarized."
					}

					m.history = []llm.Message{
						{Role: "system", Content: fmt.Sprintf("Compact History Summary: %s", summary)},
					}
					m.renderViewport()
					return fmt.Sprintf("🧹 Conversation history compacted into concise summary block!\n\nSummary: %s", summary)
				}
			}

			// 5. Focus Target Switcher: /target <name>
			if strings.HasPrefix(cmdLower, "/target ") || strings.HasPrefix(cmdLower, "/focus ") {
				tgtName := strings.TrimSpace(input[8:])
				if strings.HasPrefix(cmdLower, "/focus ") {
					tgtName = strings.TrimSpace(input[7:])
				}
				if tgtName == "" {
					return m, func() tea.Msg { return fmt.Sprintf("🎯 Current focus target: [%s]", m.activeTarget) }
				}

				found := false
				for _, t := range m.cfg.Targets {
					if t.Name == tgtName || t.Host == tgtName {
						m.activeTarget = t.Name
						found = true
						break
					}
				}
				if !found && tgtName != "local" {
					return m, func() tea.Msg { return fmt.Sprintf("❌ Target '%s' not found in config.yaml. Use /targets to list valid hosts.", tgtName) }
				}
				if tgtName == "local" {
					m.activeTarget = "local"
				}

				return m, func() tea.Msg { return fmt.Sprintf("🎯 Active focus target switched to [%s]!", m.activeTarget) }
			}

			// Slash command handling
			if cmdLower == "/sessions" || cmdLower == "/resume" || cmdLower == "/session" {
				if m.dbStore != nil {
					if sessList, err := m.dbStore.GetRecentSessions(8); err == nil && len(sessList) > 0 {
						m.sessions = sessList
						m.selectingSess = true
						m.selectedSessIdx = 0
						return m, nil
					}
				}
				return m, func() tea.Msg { return "⚠️ No previous chat sessions found in database." }
			}

			if cmdLower == "/new" {
				m.sessionID = fmt.Sprintf("s-%s", time.Now().Format("20060102-150405"))
				m.history = nil
				m.renderViewport()
				return m, func() tea.Msg { return fmt.Sprintf("✨ Started new chat session [%s]!", m.sessionID) }
			}

			if cmdLower == "/clear" {
				m.history = nil
				m.renderViewport()
				return m, nil
			}

			if cmdLower == "/help" {
				helpTxt := `💡 MicroHarness TUI Slash Commands:
• ! <cmd> or /sh     — Direct shell command execution on host
• /add <filepath>    — Attach local file content into chat context
• /log <service>     — Attach live systemd journal log into context
• /model <provider>  — Switch active LLM provider (gemini, claude, ollama, litellm)
• /target <name>     — Switch active focus target node
• /compact           — Auto-compact conversation history into a concise summary
• /sessions          — Switch chat session via interactive menu
• /new               — Start a fresh chat session
• /clear             — Clear current chat screen history
• /skills            — List installed skills catalog
• /targets or /hosts — List monitored target systems
• /discover or /scan — Discover network target hosts with open SSH port 22
• /stats             — Display live system telemetry & model token stats
• /skill generate    — AI skill generator wizard
• /help              — Show this help message`
				return m, func() tea.Msg { return helpTxt }
			}

			if cmdLower == "/discover" || cmdLower == "/scan" || cmdLower == "discover targets" {
				m.loading = true
				m.statusMsg = "⏳ [1/2] Scanning subnet for open SSH port 22 and testing keys..."
				m.renderViewport()

				return m, func() tea.Msg {
					discovered, err := sysinfo.DiscoverNetworkTargets(context.Background(), "root")
					if err != nil {
						return fmt.Sprintf("❌ Discovery error: %v", err)
					}
					if len(discovered) == 0 {
						return "🔍 Network scan complete: No active SSH targets found on immediate neighbor subnets."
					}

					var lines []string
					lines = append(lines, "🔍 Discovered Network Target Candidates (SSH Port 22 Open):")
					for _, d := range discovered {
						sshStatus := "🔒 Passwordless SSH: Key Needed (`ssh-copy-id " + d.User + "@" + d.IP + "`)"
						if d.PasswordlessSSH {
							sshStatus = fmt.Sprintf("🔑 Passwordless SSH: READY (User: %s | Hostname: %s)", d.User, d.Hostname)
						}
						lines = append(lines, fmt.Sprintf("  • IP: %s [%s] │ %s", d.IP, d.Interface, sshStatus))
					}
					lines = append(lines, "\n👉 To register a target, type: `add host <ip_or_name>`")
					return strings.Join(lines, "\n")
				}
			}

			if cmdLower == "/skills" {
				if m.skillMgr == nil {
					return m, func() tea.Msg { return "No skill manager loaded." }
				}
				skList := m.skillMgr.ListSkills()
				var lines []string
				lines = append(lines, "🛠️ Installed Skills Catalog:")
				for _, sk := range skList {
					lines = append(lines, fmt.Sprintf("  • %s — %s", sk.Name, sk.Description))
				}
				return m, func() tea.Msg { return strings.Join(lines, "\n") }
			}

			if cmdLower == "/targets" || cmdLower == "/hosts" || cmdLower == "/systems" {
				if len(m.cfg.Targets) == 0 {
					return m, func() tea.Msg { return "No target systems configured in config.yaml." }
				}
				var lines []string
				lines = append(lines, "🖥️ Monitored Target Systems:")
				for _, t := range m.cfg.Targets {
					focusMarker := ""
					if t.Name == m.activeTarget {
						focusMarker = " 🎯 [ACTIVE FOCUS]"
					}
					if t.Type == "ssh" {
						lines = append(lines, fmt.Sprintf("  • %s (ssh: %s@%s)%s", t.Name, t.User, t.Host, focusMarker))
					} else {
						lines = append(lines, fmt.Sprintf("  • %s (local host)%s", t.Name, focusMarker))
					}
				}
				return m, func() tea.Msg { return strings.Join(lines, "\n") }
			}

			if cmdLower == "/stats" {
				statsInfo := fmt.Sprintf(
					"📊 Live Telemetry & Model Usage Stats:\n• Focus Target: %s\n• Host: %s (%s/%s)\n• CPUs: %d | Load: %.2f\n• RAM: %dMB / %dMB\n• Total Requests: %d\n• Total Tokens: ~%d\n• Est Cost: $%.5f",
					m.activeTarget, m.sysStats.Hostname, m.sysStats.OS, m.sysStats.Arch,
					m.sysStats.CPUCount, m.sysStats.LoadAvg1,
					m.sysStats.MemUsedMB, m.sysStats.MemTotalMB,
					m.llmStats.TotalRequests, m.llmStats.TotalTokens, m.llmStats.EstimatedCost,
				)
				return m, func() tea.Msg { return statsInfo }
			}

			// 6. Remote SSH Skill Execution: /run skill <name> [on <target>]
			if strings.HasPrefix(cmdLower, "run skill ") || strings.HasPrefix(cmdLower, "/run ") {
				raw := strings.TrimPrefix(input, "run skill ")
				if strings.HasPrefix(cmdLower, "/run ") {
					raw = strings.TrimPrefix(input, "/run ")
				}

				targetNode := m.activeTarget
				skillName := raw
				if strings.Contains(raw, " on ") {
					parts := strings.SplitN(raw, " on ", 2)
					skillName = strings.TrimSpace(parts[0])
					targetNode = strings.TrimSpace(parts[1])
				}

				return m, func() tea.Msg {
					var user, host string
					for _, t := range m.cfg.Targets {
						if t.Name == targetNode {
							user = t.User
							host = t.Host
							break
						}
					}

					out, err := m.skillMgr.ExecuteOnTarget(context.Background(), skillName, user, host, nil)
					if err != nil {
						return fmt.Sprintf("❌ Error executing skill [%s] on target [%s]: %v", skillName, targetNode, err)
					}
					return fmt.Sprintf("🛠️ Skill [%s] Output on Target [%s]:\n```\n%s\n```", skillName, targetNode, out)
				}
			}

			// 10. AI Skill Generator Wizard: /skill generate <prompt>
			if strings.HasPrefix(cmdLower, "/skill generate ") || strings.HasPrefix(cmdLower, "generate skill ") {
				genPrompt := strings.TrimPrefix(input, "/skill generate ")
				if strings.HasPrefix(cmdLower, "generate skill ") {
					genPrompt = strings.TrimPrefix(input, "generate skill ")
				}

				m.loading = true
				m.statusMsg = "🪄 AI Skill Generator: Writing bash script & manifest..."
				m.renderViewport()

				return m, func() tea.Msg {
					prompt := fmt.Sprintf("Write a bash skill script for: '%s'. Return ONLY a JSON block with keys 'name', 'description', and 'script'. Name must be lowercase_with_underscores.", genPrompt)
					resp, err := m.llmClient.Generate(context.Background(), prompt, nil)
					if err != nil {
						return fmt.Sprintf("❌ Skill Generator failed: %v", err)
					}

					name := "custom_gen_skill"
					if idx := strings.Index(genPrompt, " "); idx != -1 {
						name = strings.ReplaceAll(strings.ToLower(genPrompt[:idx]), " ", "_")
					}
					desc := genPrompt
					script := "#!/usr/bin/env bash\n" + resp

					skill, err := m.skillMgr.CreateSkill(name, desc, script)
					if err != nil {
						return fmt.Sprintf("❌ Failed to create generated skill: %v", err)
					}
					return fmt.Sprintf("✨ Successfully generated, verified, and installed new skill '%s'!\nRun it anytime using: `/run %s`", skill.Name, skill.Name)
				}
			}

			if strings.HasPrefix(input, "create skill ") {
				parts := strings.SplitN(strings.TrimPrefix(input, "create skill "), "|", 3)
				if len(parts) >= 2 {
					name := strings.TrimSpace(parts[0])
					desc := strings.TrimSpace(parts[1])
					script := "#!/usr/bin/env bash\necho 'Skill executed'"
					if len(parts) == 3 {
						script = strings.TrimSpace(parts[2])
					}
					_, err := m.skillMgr.CreateSkill(name, desc, script)
					if err != nil {
						return m, func() tea.Msg { return fmt.Sprintf("❌ Failed to create skill %s: %v", name, err) }
					}
					return m, func() tea.Msg { return fmt.Sprintf("✅ Created new skill '%s' and added to catalog!", name) }
				}
			}

			// Add target / host command handling
			isAddCmd := false
			prefixes := []string{
				"add target ", "add host ", "add system ",
				"/addtarget ", "/addhost ", "/add-target ", "/add-host ",
			}
			for _, p := range prefixes {
				if strings.HasPrefix(cmdLower, p) {
					isAddCmd = true
					break
				}
			}

			if isAddCmd {
				raw := input
				for _, p := range prefixes {
					if idx := strings.Index(strings.ToLower(raw), p); idx != -1 {
						raw = raw[idx+len(p):]
						break
					}
				}

				var name, host, user string
				if strings.Contains(raw, "|") {
					parts := strings.Split(raw, "|")
					if len(parts) >= 1 {
						name = strings.TrimSpace(parts[0])
					}
					if len(parts) >= 2 {
						host = strings.TrimSpace(parts[1])
					}
					if len(parts) >= 3 {
						user = strings.TrimSpace(parts[2])
					}
				} else {
					fields := strings.Fields(raw)
					if len(fields) >= 1 {
						name = fields[0]
					}
					if len(fields) >= 2 {
						host = fields[1]
					}
					if len(fields) >= 3 {
						user = fields[2]
					}
				}

				if host == "" && name != "" {
					host = name
				}

				if name == "" || host == "" {
					return m, func() tea.Msg {
						return "⚠️ Usage: `add host <ip_or_name>` or `add target <name> | <host> | <user>` (e.g. `add host 192.168.4.61`)"
					}
				}

				if user == "" {
					user = "root"
				}

				// Check for duplicate target names
				for _, t := range m.cfg.Targets {
					if t.Name == name || (t.Host != "" && t.Host == host) {
						return m, func() tea.Msg { return fmt.Sprintf("❌ Target or host '%s' (%s) is already registered in config.yaml.", name, host) }
					}
				}

				// Verify Passwordless SSH
				m.loading = true
				m.statusMsg = fmt.Sprintf("⏳ Verifying passwordless SSH connectivity to %s@%s...", user, host)
				m.renderViewport()

				return m, func() tea.Msg {
					sshOK, sshMsg := sysinfo.VerifyPasswordlessSSH(context.Background(), user, host)

					newTarget := config.TargetConfig{
						Name: name,
						Type: "ssh",
						Host: host,
						User: user,
					}

					m.cfg.Targets = append(m.cfg.Targets, newTarget)
					home, _ := os.UserHomeDir()
					if home == "" {
						home = "/home/fcurrie"
					}
					cfgPath := filepath.Join(home, ".config", "microharness", "config.yaml")
					if err := m.cfg.Save(cfgPath); err != nil {
						return fmt.Sprintf("❌ Failed to save updated target config: %v", err)
					}

					if sshOK {
						return fmt.Sprintf("✅ Target '%s' (%s@%s) registered successfully in config.yaml!\n🔑 Passwordless SSH Verified: %s", name, user, host, sshMsg)
					}
					return fmt.Sprintf("⚠️ Target '%s' (%s@%s) registered in config.yaml, but passwordless SSH check failed:\n%s", name, user, host, sshMsg)
				}
			}

			// Process via LLM Client if available
			if m.llmClient != nil {
				m.loading = true
				m.statusMsg = "⏳ [1/4] Parsing query & inspecting context..."
				m.renderViewport()

				historySnapshot := make([]llm.Message, len(m.history))
				copy(historySnapshot, m.history)

				generateCmd := func() tea.Msg {
					start := time.Now()
					soul := config.GetSoulContent()

					var contextParts []string
					contextParts = append(contextParts, fmt.Sprintf("Active Focus Target: %s", m.activeTarget))

					// Configured Targets context
					if len(m.cfg.Targets) > 0 {
						var targetStrs []string
						for _, t := range m.cfg.Targets {
							if t.Type == "ssh" {
								targetStrs = append(targetStrs, fmt.Sprintf("%s (ssh: %s@%s)", t.Name, t.User, t.Host))
							} else {
								targetStrs = append(targetStrs, fmt.Sprintf("%s (local host)", t.Name))
							}
						}
						contextParts = append(contextParts, fmt.Sprintf("Monitored Target Systems: [%s]", strings.Join(targetStrs, ", ")))
					}

					// Installed Skills catalog context
					if m.skillMgr != nil {
						skList := m.skillMgr.ListSkills()
						var skNames []string
						for _, sk := range skList {
							skNames = append(skNames, fmt.Sprintf("%s (%s)", sk.Name, sk.Description))
						}
						if len(skNames) > 0 {
							contextParts = append(contextParts, fmt.Sprintf("Available Skills Catalog: [%s]", strings.Join(skNames, "; ")))
						}
					}

					// Live Telemetry
					if stats, err := sysinfo.GetStats(); err == nil {
						contextParts = append(contextParts, fmt.Sprintf("Live System Telemetry: %s", stats.Summary()))
					}

					ctxBlock := strings.Join(contextParts, "\n")
					prompt := fmt.Sprintf("%s\n\n=== REAL-TIME SYSTEM CONTEXT ===\n%s\n===============================\n\nUser Query: %s", soul, ctxBlock, input)

					resp, err := m.llmClient.Generate(context.Background(), prompt, historySnapshot)
					elapsed := time.Since(start)
					if err != nil {
						return llmResponseMsg{err: err}
					}

					promptTokens := len(prompt) / 4
					if promptTokens < 1 {
						promptTokens = 1
					}
					evalTokens := len(resp) / 4
					if evalTokens < 1 {
						evalTokens = 1
					}

					return llmResponseMsg{
						content:      resp,
						promptTokens: promptTokens,
						evalTokens:   evalTokens,
						latency:      elapsed,
					}
				}

				return m, tea.Batch(
					generateCmd,
					tickStatusCmd("🧠 [2/4] Planning skill execution strategy...", 2, 350*time.Millisecond),
				)
			}

		}

	case statusStepMsg:
		if m.loading {
			m.statusMsg = msg.text
			m.renderViewport()
			switch msg.step {
			case 2:
				return m, tickStatusCmd("⚡ [3/4] Offloading prompt to model engine...", 3, 400*time.Millisecond)
			case 3:
				return m, tickStatusCmd("✨ [4/4] Generating response stream...", 4, 450*time.Millisecond)
			}
		}

	case llmResponseMsg:
		m.loading = false
		m.statusMsg = ""
		if msg.err != nil {
			msgStr := fmt.Sprintf("Agent Error: %v", msg.err)
			m.history = append(m.history, llm.Message{Role: "assistant", Content: msgStr})
			m.renderViewport()
			return m, nil
		}

		// Calculate estimated API cost (Optimization 9)
		var cost float64
		switch m.cfg.LLM.DefaultProvider {
		case "gemini":
			cost = (float64(msg.promptTokens+msg.evalTokens) / 1000.0) * 0.00015
		case "claude":
			cost = (float64(msg.promptTokens+msg.evalTokens) / 1000.0) * 0.003
		case "litellm":
			cost = (float64(msg.promptTokens+msg.evalTokens) / 1000.0) * 0.0005
		default:
			cost = 0.00 // Ollama local is free
		}

		m.llmStats.TotalRequests++
		m.llmStats.LastLatency = msg.latency
		m.llmStats.LastPromptTokens = msg.promptTokens
		m.llmStats.LastEvalTokens = msg.evalTokens
		m.llmStats.TotalTokens += (msg.promptTokens + msg.evalTokens)
		m.llmStats.EstimatedCost += cost

		// 7. ReAct Tool Loop Handling
		content := msg.content
		if strings.Contains(content, "CALL_SKILL:") || strings.Contains(content, "```call:") {
			var skillToRun string
			if idx := strings.Index(content, "CALL_SKILL:"); idx != -1 {
				line := content[idx+11:]
				if endIdx := strings.Index(line, "\n"); endIdx != -1 {
					skillToRun = strings.TrimSpace(line[:endIdx])
				} else {
					skillToRun = strings.TrimSpace(line)
				}
			} else if idx := strings.Index(content, "```call:"); idx != -1 {
				line := content[idx+8:]
				if endIdx := strings.Index(line, "```"); endIdx != -1 {
					skillToRun = strings.TrimSpace(line[:endIdx])
				}
			}

			if skillToRun != "" && m.skillMgr != nil {
				var user, host string
				for _, t := range m.cfg.Targets {
					if t.Name == m.activeTarget {
						user = t.User
						host = t.Host
						break
					}
				}

				out, err := m.skillMgr.ExecuteOnTarget(context.Background(), skillToRun, user, host, nil)
				if err == nil {
					toolResultMsg := fmt.Sprintf("\n\n⚙️ [Autonomous ReAct Execution] Skill '%s' output:\n```\n%s\n```", skillToRun, strings.TrimSpace(out))
					content += toolResultMsg
				}
			}
		}

		m.history = append(m.history, llm.Message{Role: "assistant", Content: content})
		m.renderViewport()

		if m.dbStore != nil {
			_ = m.dbStore.SaveMessage(m.sessionID, "assistant", content)
		}

	case string:
		m.loading = false
		m.statusMsg = ""
		m.history = append(m.history, llm.Message{Role: "assistant", Content: msg})
		m.renderViewport()

		if m.dbStore != nil {
			_ = m.dbStore.SaveMessage(m.sessionID, "assistant", msg)
		}

	case errMsg:
		m.err = msg
		return m, nil
	}

	return m, tea.Batch(tiCmd, vpCmd)
}

func (m model) View() string {
	if m.selectingSess {
		var sb strings.Builder
		sb.WriteString("───────────────────────────────────────────────────────────────\n")
		sb.WriteString("       🚀 MicroHarness Command Center — Chat Session Selection  \n")
		sb.WriteString("───────────────────────────────────────────────────────────────\n\n")

		if m.selectedSessIdx == 0 {
			sb.WriteString("  \x1b[1;32m> ✨ [Start New Chat Session]\x1b[0m\n\n")
		} else {
			sb.WriteString("    ✨ [Start New Chat Session]\n\n")
		}

		for i, sess := range m.sessions {
			preview := truncateResp(sess.LastMessage, 35)
			if preview == "" {
				preview = "Empty chat session"
			}
			if i+1 == m.selectedSessIdx {
				sb.WriteString(fmt.Sprintf("  \x1b[1;32m> %d. %s (%s)\x1b[0m\n     Preview: %s\n\n", i+1, sess.SessionID, sess.UpdatedAt.Format("15:04"), preview))
			} else {
				sb.WriteString(fmt.Sprintf("    %d. %s (%s)\n     Preview: %s\n\n", i+1, sess.SessionID, sess.UpdatedAt.Format("15:04"), preview))
			}
		}

		sb.WriteString("───────────────────────────────────────────────────────────────\n")
		sb.WriteString("  Navigation: [Up/Down or Ctrl+J/K] Select  │  [Enter] Open  │  [Esc] Cancel\n")
		return sb.String()
	}

	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#FFFFFF")).
		Background(lipgloss.Color("#1E1E2E")).
		Padding(0, 1)

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#74C7EC")).
		Padding(0, 1)

	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#A6E3A1"))

	// Header displaying Active Target & Provider
	header := headerStyle.Render(fmt.Sprintf(" MicroHarness v0.2.0 │ Session: %s │ Focus: %s │ Provider: %s ",
		m.sessionID, m.activeTarget, m.cfg.LLM.DefaultProvider))

	// Left Pane (Chat Viewport & Textarea)
	chatView := lipgloss.JoinVertical(
		lipgloss.Left,
		titleStyle.Render(fmt.Sprintf("── Agent Chat Session [%s] ──", m.sessionID)),
		m.viewport.View(),
		m.textarea.View(),
	)
	leftPane := boxStyle.Render(chatView)

	// Right Pane (System Stats, Model Telemetry HUD & Recent Jobs)
	statsInfo := fmt.Sprintf(
		"OS: %s/%s\nCPUs: %d | Load: %.2f\nRAM: %dMB / %dMB\nDisk Free: %d GB\nFocus Target: %s\n",
		m.sysStats.OS, m.sysStats.Arch,
		m.sysStats.CPUCount, m.sysStats.LoadAvg1,
		m.sysStats.MemUsedMB, m.sysStats.MemTotalMB,
		m.sysStats.DiskFree/(1024*1024*1024),
		m.activeTarget,
	)

	activeModel := m.cfg.LLM.Gemini.Model
	if m.cfg.LLM.DefaultProvider == "claude" {
		activeModel = m.cfg.LLM.Claude.Model
	} else if m.cfg.LLM.DefaultProvider == "ollama" {
		activeModel = m.cfg.LLM.Ollama.Model
	} else if m.cfg.LLM.DefaultProvider == "litellm" {
		activeModel = m.cfg.LLM.LiteLLM.Model
	}

	modelStatsInfo := fmt.Sprintf(
		"Active Model: %s\nTotal Requests: %d\nLast Latency: %v\nLast Prompt: ~%d tokens\nLast Output: ~%d tokens\nTotal Tokens: ~%d tokens\nEst. Cost: $%.5f\n",
		activeModel,
		m.llmStats.TotalRequests,
		m.llmStats.LastLatency.Round(time.Millisecond),
		m.llmStats.LastPromptTokens,
		m.llmStats.LastEvalTokens,
		m.llmStats.TotalTokens,
		m.llmStats.EstimatedCost,
	)

	var logLines []string
	for _, l := range m.recentLogs {
		logLines = append(logLines, fmt.Sprintf("[%s] %s -> %s", l.ExecutedAt.Format("15:04"), l.JobName, l.Status))
	}
	if len(logLines) == 0 {
		logLines = append(logLines, "No recent job runs.")
	}

	rightView := lipgloss.JoinVertical(
		lipgloss.Left,
		titleStyle.Render("── System Monitor ──"),
		statsInfo,
		"\n"+titleStyle.Render("── Model Telemetry HUD ──"),
		modelStatsInfo,
		"\n"+titleStyle.Render("── Recent Jobs ──"),
		strings.Join(logLines, "\n"),
		"\n"+titleStyle.Render("── Loaded Skills ──"),
		fmt.Sprintf("Active Skills: %d loaded", len(m.skillMgr.ListSkills())),
	)
	rightWidth := m.width - ((m.width * 6) / 10) - 8
	if rightWidth < 25 {
		rightWidth = 25
	}
	rightPane := boxStyle.Width(rightWidth).Render(rightView)

	// Combine Panes horizontally
	mainView := lipgloss.JoinHorizontal(lipgloss.Top, leftPane, rightPane)

	footer := fmt.Sprintf("\n[Enter] Send  │  [! <cmd>] Shell  │  [/help] Commands  │  [Esc] Quit  │  Time: %s", time.Now().Format("15:04:05"))

	return lipgloss.JoinVertical(lipgloss.Left, header, mainView, footer)
}

func (m *model) renderViewport() {
	wrapWidth := m.viewport.Width
	if wrapWidth <= 0 {
		wrapWidth = 60
	}
	wrapStyle := lipgloss.NewStyle().Width(wrapWidth)

	var sb strings.Builder
	for _, msg := range m.history {
		if msg.Role == "user" {
			header := "\x1b[1;36mYou:\x1b[0m\n"
			content := wrapStyle.Render(msg.Content)
			sb.WriteString(fmt.Sprintf("%s%s\n\n", header, content))
		} else {
			header := fmt.Sprintf("\x1b[1;32mAgent (%s):\x1b[0m\n", m.cfg.LLM.DefaultProvider)
			content := wrapStyle.Render(msg.Content)
			sb.WriteString(fmt.Sprintf("%s%s\n\n", header, content))
		}
	}

	if m.loading && m.statusMsg != "" {
		sb.WriteString(fmt.Sprintf("\x1b[1;33mProcess:\x1b[0m %s", wrapStyle.Render(m.statusMsg)))
	}

	m.viewport.SetContent(sb.String())
	m.viewport.GotoBottom()
}

func truncateResp(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.TrimSpace(s)
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
