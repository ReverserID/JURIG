package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/imtaqin/jurig/internal/config"
	"github.com/imtaqin/jurig/internal/llm"
	"github.com/imtaqin/jurig/internal/portable"
)

// wizardStep is the current page of the setup wizard.
type wizardStep int

const (
	stepProvider wizardStep = iota
	stepBaseURL
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
	needsURL bool // prompt for a base URL (custom OpenAI-compatible endpoint)
}

var wizardProviders = []provChoice{
	{"openrouter", "OpenRouter — 400+ models (default)", true, false},
	{"kimi", "Kimi Code — key sk-kimi- (api.kimi.com)", true, false},
	{"moonshot", "Moonshot — key sk- (api.moonshot.ai)", true, false},
	{"mimo", "Xiaomi MiMo (mimo-v2.5-pro, off-peak 20% off)", true, false},
	{"dashscope", "Qwen / Alibaba DashScope", true, false},
	{"ollama", "Ollama — local, no key", false, false},
	{"anthropic", "Anthropic API — direct key", true, false},
	{"custom", "Custom — OpenAI-compatible base URL (9router/OmniRoute/vLLM/…)", true, true},
	{"claude-cli", "Claude subscription — Claude Code, no key", false, false},
}

// wizardModel is the first-run setup flow.
type wizardModel struct {
	cfg          *config.Config
	cfgPath      string
	step         wizardStep
	cursor       int
	key          textinput.Model
	baseIn       textinput.Model
	modelIn      textinput.Model
	models       []string // preset models for the chosen provider
	chosen       provChoice
	installTools bool
	installCh    chan installMsg
	log          []string
	saved        bool
	w            int
	fetching     bool   // a /models fetch is in flight
	fetchErr     string // last fetch error, shown on the model step
	mcursor      int    // selected row in the (filtered) model list
	chosenModel  string // the model id confirmed on the model step
}

// modelListHeight is how many model rows the wizard shows at once (windowed).
const modelListHeight = 12

// filteredModels returns models matching the current filter box (case-insensitive
// substring). Empty filter → all.
func (m *wizardModel) filteredModels() []string {
	q := strings.ToLower(strings.TrimSpace(m.modelIn.Value()))
	if q == "" {
		return m.models
	}
	var out []string
	for _, id := range m.models {
		if strings.Contains(strings.ToLower(id), q) {
			out = append(out, id)
		}
	}
	return out
}

// installMsg streams tool-install progress into the wizard.
type installMsg struct {
	line string
	done bool
	err  error
}

// wizModelsMsg carries a live /models fetch result into the model step.
type wizModelsMsg struct {
	models []string
	err    error
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

	bi := textinput.New()
	bi.Placeholder = "http://localhost:20128/v1"
	bi.CharLimit = 300

	m := &wizardModel{cfg: cfg, cfgPath: cfgPath, key: k, baseIn: bi, modelIn: mi}
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
	case wizModelsMsg:
		m.fetching = false
		if msg.err != nil {
			m.fetchErr = msg.err.Error()
			return m, nil
		}
		m.fetchErr = ""
		if len(msg.models) > 0 {
			m.models = msg.models
			m.mcursor = 0
		}
		return m, nil
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
		case stepBaseURL:
			m.baseIn, cmd = m.baseIn.Update(msg)
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
	case stepBaseURL:
		m.baseIn, cmd = m.baseIn.Update(msg)
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
			switch {
			case m.chosen.needsURL:
				m.baseIn.SetValue(m.cfg.Providers[m.chosen.key].BaseURL)
				m.baseIn.Focus()
				m.step = stepBaseURL
			case m.chosen.needsKey:
				m.key.SetValue(m.cfg.Providers[m.chosen.key].APIKey)
				m.key.Focus()
				m.step = stepKey
			default:
				return m, m.enterModelStep(), true
			}
		}
		return m, nil, true // provider list consumes every key

	case stepBaseURL:
		switch msg.String() {
		case "enter":
			if strings.TrimSpace(m.baseIn.Value()) == "" {
				return m, nil, true // require a URL before advancing
			}
			if m.chosen.needsKey {
				m.key.SetValue(m.cfg.Providers[m.chosen.key].APIKey)
				m.key.Focus()
				m.step = stepKey
				return m, nil, true
			}
			return m, m.enterModelStep(), true
		case "esc":
			m.step = stepProvider
			return m, nil, true
		}
		return m, nil, false // let URL text flow to the input (incl. paste)

	case stepKey:
		switch msg.String() {
		case "enter":
			return m, m.enterModelStep(), true
		case "esc":
			if m.chosen.needsURL {
				m.step = stepBaseURL
			} else {
				m.step = stepProvider
			}
			return m, nil, true
		}
		return m, nil, false // let key text flow to the input (incl. paste)

	case stepModel:
		switch msg.String() {
		case "up", "ctrl+p":
			if m.mcursor > 0 {
				m.mcursor--
			}
			return m, nil, true
		case "down", "ctrl+n":
			if m.mcursor < len(m.filteredModels())-1 {
				m.mcursor++
			}
			return m, nil, true
		case "enter":
			// Prefer the highlighted list row; else whatever was typed.
			pick := strings.TrimSpace(m.modelIn.Value())
			if fl := m.filteredModels(); len(fl) > 0 && m.mcursor < len(fl) {
				pick = fl[m.mcursor]
				m.chosenModel = pick
			} else {
				m.chosenModel = pick
			}
			if pick != "" {
				m.step = stepTools
				m.cursor = 0
			}
			return m, nil, true
		case "esc":
			m.step = stepProvider
			return m, nil, true
		}
		// Any other key = typing into the filter box → reset selection.
		m.mcursor = 0
		return m, nil, false

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

func (m *wizardModel) enterModelStep() tea.Cmd {
	m.step = stepModel
	m.cursor = 0
	m.mcursor = 0
	m.modelIn.Reset()
	m.modelIn.Placeholder = "type to filter · or a custom model id"
	m.modelIn.Focus()
	return m.fetchModelsCmd()
}

// fetchModelsCmd pulls the model catalog from an OpenAI-compatible endpoint when
// the chosen provider has a base URL (custom, ollama, etc.). Returns nil if the
// provider can't be listed (e.g. anthropic, claude-cli).
func (m *wizardModel) fetchModelsCmd() tea.Cmd {
	pc := m.cfg.Providers[m.chosen.key]
	if pc.Kind != config.KindOpenAI {
		return nil
	}
	base := pc.BaseURL
	if m.chosen.needsURL {
		base = strings.TrimSpace(m.baseIn.Value())
	}
	key := pc.APIKey
	if m.chosen.needsKey {
		key = strings.TrimSpace(m.key.Value())
	}
	if base == "" {
		return nil
	}
	m.fetching = true
	m.fetchErr = ""
	name := m.chosen.key
	return func() tea.Msg {
		ids, err := llm.NewOpenAILister(name, base, key).ListModels(context.Background())
		return wizModelsMsg{models: ids, err: err}
	}
}

// applyAndMaybeInstall records the selection into cfg and either installs
// tools (streaming) or finishes immediately.
func (m *wizardModel) applyAndMaybeInstall() tea.Cmd {
	// write provider base URL + key + active selection into config
	pc := m.cfg.Providers[m.chosen.key]
	if m.chosen.needsURL {
		pc.BaseURL = strings.TrimSpace(m.baseIn.Value())
	}
	if m.chosen.needsKey {
		pc.APIKey = strings.TrimSpace(m.key.Value())
	}
	m.cfg.Providers[m.chosen.key] = pc
	model := strings.TrimSpace(m.chosenModel)
	if model == "" {
		model = strings.TrimSpace(m.modelIn.Value())
	}
	if model == "" && len(m.models) > 0 {
		model = m.models[0]
	}
	m.cfg.Active = config.Selection{Provider: m.chosen.key, Model: model}

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

	case stepBaseURL:
		b.WriteString(fmt.Sprintf("Base URL for %s (OpenAI-compatible endpoint):\n\n", cmdStyle.Render(m.chosen.key)))
		b.WriteString("  " + m.baseIn.View() + "\n\n")
		b.WriteString(statusStyle.Render("e.g. 9router/OmniRoute → http://localhost:20128/v1 · vLLM/LM Studio → your host") + "\n")
		b.WriteString(statusStyle.Render("Enter next · Esc back"))

	case stepKey:
		b.WriteString(fmt.Sprintf("API key for %s:\n\n", cmdStyle.Render(m.chosen.key)))
		b.WriteString("  " + m.key.View() + "\n\n")
		b.WriteString(statusStyle.Render(m.keyHint()))
		b.WriteString("\n" + statusStyle.Render("Enter next · Esc back"))

	case stepModel:
		b.WriteString(fmt.Sprintf("Model for %s:\n\n", cmdStyle.Render(m.chosen.key)))
		b.WriteString("  filter › " + m.modelIn.View() + "\n\n")
		switch {
		case m.fetching:
			b.WriteString(neonStyle.Render("  ↻ loading models from endpoint…") + "\n")
		case m.fetchErr != "":
			b.WriteString(errStyle.Render("  models: "+m.fetchErr) + "\n")
			b.WriteString(statusStyle.Render("  (type a model id in the filter box, then Enter)") + "\n")
		default:
			b.WriteString(m.modelListView())
		}
		b.WriteString("\n" + statusStyle.Render("↑/↓ select · type to filter · Enter choose · Esc back"))

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

// modelListView renders the filtered model list as a windowed, arrow-selectable
// list with scroll indicators.
func (m *wizardModel) modelListView() string {
	fl := m.filteredModels()
	if len(fl) == 0 {
		if len(m.models) == 0 {
			return statusStyle.Render("  (no models — type an id in the filter box)") + "\n"
		}
		return statusStyle.Render("  (no match — clear the filter or type a custom id)") + "\n"
	}
	if m.mcursor >= len(fl) {
		m.mcursor = len(fl) - 1
	}
	// Compute a scroll window centered-ish on the cursor.
	start := 0
	if m.mcursor >= modelListHeight {
		start = m.mcursor - modelListHeight + 1
	}
	end := start + modelListHeight
	if end > len(fl) {
		end = len(fl)
	}

	var b strings.Builder
	if start > 0 {
		b.WriteString(statusStyle.Render(fmt.Sprintf("  ↑ %d more", start)) + "\n")
	}
	for i := start; i < end; i++ {
		if i == m.mcursor {
			b.WriteString(selStyle.Render("› "+fl[i]) + "\n")
		} else {
			b.WriteString("  " + fl[i] + "\n")
		}
	}
	if end < len(fl) {
		b.WriteString(statusStyle.Render(fmt.Sprintf("  ↓ %d more", len(fl)-end)) + "\n")
	}
	b.WriteString(statusStyle.Render(fmt.Sprintf("  %d/%d models", m.mcursor+1, len(fl))) + "\n")
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
	case "mimo":
		return "MiMo API key (mimo.xiaomimimo.com) — set MIMO_API_KEY"
	case "dashscope":
		return "Alibaba Model Studio → API key (DASHSCOPE_API_KEY)"
	case "anthropic":
		return "console.anthropic.com — note: API billing, not subscription"
	case "custom":
		return "leave blank for a keyless local gateway; else paste the endpoint's key"
	}
	return ""
}

func tail(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
