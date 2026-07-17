package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/imtaqin/jurig/internal/config"
	"github.com/imtaqin/jurig/internal/portable"
)

// wizardStep is the current page of the setup wizard.
type wizardStep int

const (
	stepProvider wizardStep = iota
	stepKey
	stepModel
	stepTools
	stepInstalling
	stepDone
)

// provChoice is one selectable provider in the wizard.
type provChoice struct {
	key      string // config provider name
	label    string
	needsKey bool
}

var wizardProviders = []provChoice{
	{"openrouter", "OpenRouter — 400+ models (default)", true},
	{"kimi", "Kimi Code — key sk-kimi- (api.kimi.com)", true},
	{"moonshot", "Moonshot — key sk- (api.moonshot.ai)", true},
	{"dashscope", "Qwen / Alibaba DashScope", true},
	{"ollama", "Ollama — local, no key", false},
	{"anthropic", "Anthropic API — direct key", true},
	{"claude-cli", "Claude subscription — Claude Code, no key", false},
}

// wizardModel is the first-run setup flow.
type wizardModel struct {
	cfg          *config.Config
	cfgPath      string
	step         wizardStep
	cursor       int
	key          textinput.Model
	modelIn      textinput.Model
	models       []string // preset models for the chosen provider
	chosen       provChoice
	installTools bool
	installCh    chan installMsg
	log          []string
	saved        bool
	w            int
}

// installMsg streams tool-install progress into the wizard.
type installMsg struct {
	line string
	done bool
	err  error
}

// RunWizard runs the interactive setup, saving config on completion.
// Returns true if setup finished (config saved).
func RunWizard(cfg *config.Config, cfgPath string) (bool, error) {
	k := textinput.New()
	k.Placeholder = "paste API key"
	k.EchoMode = textinput.EchoPassword
	k.CharLimit = 200

	mi := textinput.New()
	mi.Placeholder = "model id"
	mi.CharLimit = 100

	m := &wizardModel{cfg: cfg, cfgPath: cfgPath, key: k, modelIn: mi}
	// preselect the current active provider if valid
	for i, p := range wizardProviders {
		if p.key == cfg.Active.Provider {
			m.cursor = i
		}
	}
	prog := tea.NewProgram(m, tea.WithAltScreen())
	fm, err := prog.Run()
	if err != nil {
		return false, err
	}
	return fm.(*wizardModel).saved, nil
}

func (m *wizardModel) Init() tea.Cmd { return textinput.Blink }

func (m *wizardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w = msg.Width
	case installMsg:
		if msg.line != "" {
			m.log = append(m.log, msg.line)
		}
		if msg.done {
			m.finish()
			return m, tea.Quit
		}
		return m, m.waitInstall()
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		mdl, cmd, consumed := m.handleKey(msg)
		if consumed {
			return mdl, cmd
		}
		// Not a navigation/control key → forward to the active text input so
		// typing AND paste work on the key/model steps.
		switch m.step {
		case stepKey:
			m.key, cmd = m.key.Update(msg)
		case stepModel:
			m.modelIn, cmd = m.modelIn.Update(msg)
		}
		return m, cmd
	}
	// route non-key messages to active textinput (blink, paste events, etc.)
	var cmd tea.Cmd
	switch m.step {
	case stepKey:
		m.key, cmd = m.key.Update(msg)
	case stepModel:
		m.modelIn, cmd = m.modelIn.Update(msg)
	}
	return m, cmd
}

// handleKey processes navigation/control keys. The bool reports whether the
// key was consumed; if false, the caller forwards it to the active text input.
func (m *wizardModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	switch m.step {

	case stepProvider:
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(wizardProviders)-1 {
				m.cursor++
			}
		case "enter":
			m.chosen = wizardProviders[m.cursor]
			m.models = m.cfg.Providers[m.chosen.key].Models
			if m.chosen.needsKey {
				m.key.SetValue(m.cfg.Providers[m.chosen.key].APIKey)
				m.key.Focus()
				m.step = stepKey
			} else {
				m.enterModelStep()
			}
		}
		return m, nil, true // provider list consumes every key

	case stepKey:
		switch msg.String() {
		case "enter":
			m.enterModelStep()
			return m, nil, true
		case "esc":
			m.step = stepProvider
			return m, nil, true
		}
		return m, nil, false // let key text flow to the input (incl. paste)

	case stepModel:
		switch msg.String() {
		case "up", "ctrl+p":
			m.cycleModel(-1)
			return m, nil, true
		case "down", "ctrl+n":
			m.cycleModel(1)
			return m, nil, true
		case "enter":
			if strings.TrimSpace(m.modelIn.Value()) != "" {
				m.step = stepTools
				m.cursor = 0
			}
			return m, nil, true
		case "esc":
			m.step = stepProvider
			return m, nil, true
		}
		return m, nil, false // typing a custom model id

	case stepTools:
		switch msg.String() {
		case "up", "down", "k", "j":
			m.cursor = 1 - m.cursor
		case "enter":
			m.installTools = m.cursor == 0
			return m, m.applyAndMaybeInstall(), true
		}
		return m, nil, true
	}
	return m, nil, true
}

func (m *wizardModel) enterModelStep() {
	m.step = stepModel
	m.cursor = 0
	if len(m.models) > 0 {
		m.modelIn.SetValue(m.models[0])
	}
	m.modelIn.Focus()
}

func (m *wizardModel) cycleModel(d int) {
	if len(m.models) == 0 {
		return
	}
	m.cursor = (m.cursor + d + len(m.models)) % len(m.models)
	m.modelIn.SetValue(m.models[m.cursor])
}

// applyAndMaybeInstall records the selection into cfg and either installs
// tools (streaming) or finishes immediately.
func (m *wizardModel) applyAndMaybeInstall() tea.Cmd {
	// write provider key + active selection into config
	pc := m.cfg.Providers[m.chosen.key]
	if m.chosen.needsKey {
		pc.APIKey = strings.TrimSpace(m.key.Value())
		m.cfg.Providers[m.chosen.key] = pc
	}
	m.cfg.Active = config.Selection{Provider: m.chosen.key, Model: strings.TrimSpace(m.modelIn.Value())}

	if !m.installTools {
		m.finish()
		return tea.Quit
	}
	m.step = stepInstalling
	m.installCh = make(chan installMsg, 16)
	pm := portable.New(m.cfg.ToolsDir)
	go func() {
		for _, t := range []string{"jadx", "apktool"} {
			m.installCh <- installMsg{line: "installing " + t + " …"}
			_, err := pm.Install(t, func(s string) { m.installCh <- installMsg{line: "  " + s} })
			if err != nil {
				m.installCh <- installMsg{line: "  " + t + " failed: " + err.Error()}
			}
		}
		m.installCh <- installMsg{done: true}
	}()
	return m.waitInstall()
}

func (m *wizardModel) waitInstall() tea.Cmd {
	ch := m.installCh
	return func() tea.Msg { return <-ch }
}

func (m *wizardModel) finish() {
	if err := m.cfg.Save(m.cfgPath); err == nil {
		m.saved = true
	}
	m.step = stepDone
}

func (m *wizardModel) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(" JURIG SETUP ") + "\n\n")

	switch m.step {
	case stepProvider:
		b.WriteString("Choose LLM provider:\n\n")
		for i, p := range wizardProviders {
			line := "  " + p.label
			if i == m.cursor {
				line = selStyle.Render("› " + p.label)
			}
			b.WriteString(line + "\n")
		}
		b.WriteString("\n" + statusStyle.Render("↑/↓ select · Enter next · Ctrl+C quit"))

	case stepKey:
		b.WriteString(fmt.Sprintf("API key for %s:\n\n", cmdStyle.Render(m.chosen.key)))
		b.WriteString("  " + m.key.View() + "\n\n")
		b.WriteString(statusStyle.Render(m.keyHint()))
		b.WriteString("\n" + statusStyle.Render("Enter next · Esc back"))

	case stepModel:
		b.WriteString(fmt.Sprintf("Model for %s (↑/↓ presets, or type a custom id):\n\n", cmdStyle.Render(m.chosen.key)))
		b.WriteString("  " + m.modelIn.View() + "\n\n")
		if len(m.models) > 0 {
			b.WriteString(statusStyle.Render("presets: "+strings.Join(m.models, ", ")) + "\n")
		}
		b.WriteString(statusStyle.Render("Enter next · Esc back"))

	case stepTools:
		b.WriteString("Pre-install Android tools now? (jadx + apktool)\n")
		b.WriteString(statusStyle.Render("(either way, missing tools auto-install on demand during a run)") + "\n\n")
		opts := []string{"Yes — download now", "No — install on demand"}
		for i, o := range opts {
			line := "  " + o
			if i == m.cursor {
				line = selStyle.Render("› " + o)
			}
			b.WriteString(line + "\n")
		}
		b.WriteString("\n" + statusStyle.Render("↑/↓ · Enter finish"))

	case stepInstalling:
		b.WriteString("Installing tools…\n\n")
		for _, l := range tail(m.log, 12) {
			b.WriteString(statusStyle.Render(l) + "\n")
		}

	case stepDone:
		b.WriteString(cmdStyle.Render("✓ setup saved") + "\n\n")
		b.WriteString("  provider: " + m.cfg.Active.Provider + "\n")
		b.WriteString("  model:    " + m.cfg.Active.Model + "\n")
		b.WriteString("  config:   " + m.cfgPath + "\n")
	}
	return b.String()
}

func (m *wizardModel) keyHint() string {
	switch m.chosen.key {
	case "openrouter":
		return "get one at openrouter.ai/keys"
	case "kimi":
		return "platform.kimi.ai — key starts with sk-kimi-"
	case "moonshot":
		return "platform.moonshot.ai — key starts with sk- (not sk-kimi-)"
	case "dashscope":
		return "Alibaba Model Studio → API key (DASHSCOPE_API_KEY)"
	case "anthropic":
		return "console.anthropic.com — note: API billing, not subscription"
	}
	return ""
}

func tail(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
