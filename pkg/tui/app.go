package tui

import (
	"context"
	"fmt"
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
	width           int
	height          int
	loading         bool
}

func NewModel(cfg *config.Config, llmClient llm.Client, skillMgr *skills.Manager, dbStore *store.Store) model {
	ta := textarea.New()
	ta.Placeholder = "Type a prompt or /help for slash commands (/sessions, /new, /skills, /clear)..."
	ta.Focus()
	ta.Prompt = "│ "
	ta.CharLimit = 500
	ta.SetWidth(50)
	ta.SetHeight(3)

	vp := viewport.New(50, 15)
	vp.SetContent("Welcome to MicroHarness ASCII Command Center.\nType a message to chat with your agent or invoke system skills.\n" + strings.Repeat("─", 45) + "\n")

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
		textarea:        ta,
		viewport:        vp,
		sysStats:        stats,
		recentLogs:      logs,
		sessionID:       defaultSessID,
		sessions:        recentSessions,
		selectingSess:   selectingSess,
		selectedSessIdx: 0,
	}
	m.renderViewport()
	return m
}

func (m model) Init() tea.Cmd {
	return textarea.Blink
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.selectingSess {
		switch msg := msg.(type) {
		case tea.KeyMsg:
			switch msg.Type {
			case tea.KeyCtrlC, tea.KeyEsc:
				return m, tea.Quit
			case tea.KeyUp, tea.KeyCtrlP:
				if m.selectedSessIdx > 0 {
					m.selectedSessIdx--
				}
			case tea.KeyDown, tea.KeyCtrlN:
				if m.selectedSessIdx < len(m.sessions) {
					m.selectedSessIdx++
				}
			case tea.KeyEnter:
				if m.selectedSessIdx == 0 {
					// Start New Session
					m.sessionID = fmt.Sprintf("s-%s", time.Now().Format("20060102-150405"))
					m.history = nil
				} else {
					// Resume Selected Session
					selectedSess := m.sessions[m.selectedSessIdx-1]
					m.sessionID = selectedSess.SessionID
					m.history = nil
					if m.dbStore != nil {
						if chatMsgs, err := m.dbStore.GetMessagesBySession(m.sessionID, 50); err == nil {
							for _, cm := range chatMsgs {
								m.history = append(m.history, llm.Message{
									Role:    cm.Role,
									Content: cm.Content,
								})
							}
						}
					}
				}
				m.selectingSess = false
				m.renderViewport()
				return m, nil
			}
		case tea.WindowSizeMsg:
			m.width = msg.Width
			m.height = msg.Height
		}
		return m, nil
	}
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

		leftWidth := (msg.Width * 6) / 10
		if leftWidth < 35 {
			leftWidth = 35
		}
		m.viewport.Width = leftWidth - 4
		vpHeight := msg.Height - 12
		if vpHeight < 5 {
			vpHeight = 5
		}
		m.viewport.Height = vpHeight
		m.textarea.SetWidth(leftWidth - 4)
		m.renderViewport()

	case tea.KeyMsg:
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

			// Slash command handling
			cmdLower := strings.ToLower(input)
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
• /sessions or /resume — Switch chat session via interactive menu
• /new               — Start a fresh chat session
• /clear             — Clear current chat screen history
• /skills            — List installed skills catalog
• /targets           — List monitored target systems
• /stats             — Display live system telemetry & model token stats
• /help              — Show this help message`
				return m, func() tea.Msg { return helpTxt }
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

			if cmdLower == "/targets" {
				if len(m.cfg.Targets) == 0 {
					return m, func() tea.Msg { return "No target systems configured in config.yaml." }
				}
				var lines []string
				lines = append(lines, "🖥️ Monitored Target Systems:")
				for _, t := range m.cfg.Targets {
					if t.Type == "ssh" {
						lines = append(lines, fmt.Sprintf("  • %s (ssh: %s@%s)", t.Name, t.User, t.Host))
					} else {
						lines = append(lines, fmt.Sprintf("  • %s (local host)", t.Name))
					}
				}
				return m, func() tea.Msg { return strings.Join(lines, "\n") }
			}

			if cmdLower == "/stats" {
				statsInfo := fmt.Sprintf(
					"📊 Live Telemetry & Model Usage Stats:\n• Host: %s (%s/%s)\n• CPUs: %d | Load: %.2f\n• RAM: %dMB / %dMB\n• Total Requests: %d\n• Total Tokens: ~%d",
					m.sysStats.Hostname, m.sysStats.OS, m.sysStats.Arch,
					m.sysStats.CPUCount, m.sysStats.LoadAvg1,
					m.sysStats.MemUsedMB, m.sysStats.MemTotalMB,
					m.llmStats.TotalRequests, m.llmStats.TotalTokens,
				)
				return m, func() tea.Msg { return statsInfo }
			}

			// Check for direct skill invocation or creation
			if strings.HasPrefix(input, "run skill ") {
				skillName := strings.TrimPrefix(input, "run skill ")
				return m, func() tea.Msg {
					out, err := m.skillMgr.Execute(context.Background(), skillName, nil)
					if err != nil {
						return fmt.Sprintf("Error executing skill %s: %v", skillName, err)
					}
					return fmt.Sprintf("Skill [%s] Output:\n%s", skillName, out)
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

		m.llmStats.TotalRequests++
		m.llmStats.LastLatency = msg.latency
		m.llmStats.LastPromptTokens = msg.promptTokens
		m.llmStats.LastEvalTokens = msg.evalTokens
		m.llmStats.TotalTokens += (msg.promptTokens + msg.evalTokens)

		m.history = append(m.history, llm.Message{Role: "assistant", Content: msg.content})
		m.renderViewport()

		if m.dbStore != nil {
			_ = m.dbStore.SaveMessage(m.sessionID, "assistant", msg.content)
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
			timeStr := sess.UpdatedAt.Format("Jan 02 15:04")
			if i+1 == m.selectedSessIdx {
				sb.WriteString(fmt.Sprintf("  \x1b[1;36m> 📂 Resume Session [%s] (%d msgs) — %s — %q\x1b[0m\n", sess.SessionID, sess.MessageCount, timeStr, preview))
			} else {
				sb.WriteString(fmt.Sprintf("    📂 Resume Session [%s] (%d msgs) — %s — %q\n", sess.SessionID, sess.MessageCount, timeStr, preview))
			}
		}

		sb.WriteString("\n───────────────────────────────────────────────────────────────\n")
		sb.WriteString("\x1b[90m[↑/↓] Navigate  │  [Enter] Select Session  │  [Esc] Quit\x1b[0m\n")

		modalStyle := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#5A56E0")).
			Padding(1, 2)

		w := m.width
		h := m.height
		if w <= 0 {
			w = 80
		}
		if h <= 0 {
			h = 24
		}

		return lipgloss.Place(
			w, h,
			lipgloss.Center, lipgloss.Center,
			modalStyle.Render(sb.String()),
		)
	}

	// Styles
	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#FAFAFA")).
		Background(lipgloss.Color("#5A56E0")).
		Padding(0, 1).
		MarginBottom(1)

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#5A56E0")).
		Padding(0, 1)

	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#7D56F4"))

	// Header
	header := headerStyle.Render(fmt.Sprintf(" MicroHarness v0.1.0 │ Session: %s │ Host: %s │ Provider: %s ",
		m.sessionID, m.sysStats.Hostname, m.cfg.LLM.DefaultProvider))

	// Left Pane (Chat Viewport & Textarea)
	chatView := lipgloss.JoinVertical(
		lipgloss.Left,
		titleStyle.Render(fmt.Sprintf("── Agent Chat Session [%s] ──", m.sessionID)),
		m.viewport.View(),
		m.textarea.View(),
	)
	leftPane := boxStyle.Render(chatView)

	// Right Pane (System Stats, Model Access Stats & Job History)
	statsInfo := fmt.Sprintf(
		"OS: %s/%s\nCPUs: %d | Load: %.2f\nRAM: %dMB / %dMB\nDisk Free: %d GB\n",
		m.sysStats.OS, m.sysStats.Arch,
		m.sysStats.CPUCount, m.sysStats.LoadAvg1,
		m.sysStats.MemUsedMB, m.sysStats.MemTotalMB,
		m.sysStats.DiskFree/(1024*1024*1024),
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
		"Active Model: %s\nTotal Requests: %d\nLast Latency: %v\nLast Prompt: ~%d tokens\nLast Output: ~%d tokens\nTotal Tokens: ~%d tokens\n",
		activeModel,
		m.llmStats.TotalRequests,
		m.llmStats.LastLatency.Round(time.Millisecond),
		m.llmStats.LastPromptTokens,
		m.llmStats.LastEvalTokens,
		m.llmStats.TotalTokens,
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
		"\n"+titleStyle.Render("── Model Access Stats ──"),
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

	footer := fmt.Sprintf("\n[Enter] Send Message  │  [/help] Commands  │  [Esc] Quit  │  Time: %s", time.Now().Format("15:04:05"))

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
