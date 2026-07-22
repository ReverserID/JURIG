// Package tui renders Jurig's interactive terminal UI with Bubble Tea,
// styling chrome via Lipgloss and agent reports via Glamour.
package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/wrap"

	"github.com/imtaqin/jurig/internal/agent"
	"github.com/imtaqin/jurig/internal/config"
	"github.com/imtaqin/jurig/internal/highlight"
	"github.com/imtaqin/jurig/internal/llm"
	"github.com/imtaqin/jurig/internal/proxy"
)

var (
	cAccent = lipgloss.Color("#7D56F4")
	cGreen  = lipgloss.Color("#43BF6D")
	cNeon   = lipgloss.Color("#00FF9C")
	cYellow = lipgloss.Color("#E2C08D")
	cRed    = lipgloss.Color("#E06C75")
	cDim    = lipgloss.Color("#6C7086")

	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFFFFF")).Background(cAccent).Padding(0, 1)
	statusStyle = lipgloss.NewStyle().Foreground(cDim)
	toolStyle   = lipgloss.NewStyle().Bold(true).Foreground(cNeon)
	cmdStyle    = lipgloss.NewStyle().Foreground(cGreen)
	neonStyle   = lipgloss.NewStyle().Foreground(cNeon)
	errStyle    = lipgloss.NewStyle().Foreground(cRed)
	userStyle   = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	borderStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cAccent)
	selStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFFFFF")).Background(cAccent)
)

// toolGlyph tags each tool with a console glyph for the hacker aesthetic.
var toolGlyph = map[string]string{
	"shell": "»", "unzip": "⇲", "read_file": "▤", "write_file": "✎",
	"list_dir": "▦", "strings": "≣", "radare2": "⌗", "jadx": "⬡",
	"apktool": "⬢", "adb": "▮", "frida": "☰",
}

func glyph(tool string) string {
	if g, ok := toolGlyph[tool]; ok {
		return g
	}
	return "⚙"
}

type eventMsg agent.Event
type runDoneMsg struct{ err error }

// AskReq is a question the running agent poses to the operator, with a Reply
// channel the TUI writes the answer back on.
type AskReq struct {
	Question string
	Options  []string
	Reply    chan string
}
type askMsg AskReq

type model struct {
	ag          *agent.Agent
	router      *llm.Router
	toolStat    map[string]string
	sessionPath string
	resumed     int

	vp    viewport.Model
	ti    textinput.Model
	askIn textinput.Model
	spin  spinner.Model
	glam  *glamour.TermRenderer
	frame int // header animation frame

	// agent → operator questions
	askCh     chan AskReq
	asking    bool
	askReq    AskReq
	askCursor int
	askSel    map[int]bool
	buf       strings.Builder
	ready     bool
	w, h      int

	running  bool
	status   string
	curOp    string
	lastRead string // path of the file the AI is currently reading
	tokIn    int    // cumulative token usage
	tokOut   int
	events   chan agent.Event
	done     chan error
	cancel   context.CancelFunc

	// input history (↑/↓ recall previous prompts)
	hist    []string
	histIdx int

	// model picker overlay
	picking bool
	choices []llm.Choice
	pcursor int

	// key prompt (shown when a provider needs a key)
	keyPrompt   bool
	keyInput    textinput.Model
	keyForProv  string // provider name waiting for key
	keyForModel string // model to auto-select after key is set

	// transcript search + help overlay
	searching bool
	searchIn  textinput.Model
	lastQuery string
	matches   []int
	matchCur  int
	helpOn    bool

	// live MITM proxy side-panel
	proxy   *proxy.Manager
	panelOn bool
	panelW  int
	glamW   int

	// config persistence (for saving API keys from TUI)
	cfg     *config.Config
	cfgPath string
}

// New builds the TUI program. sessionPath is where the conversation + prompt
// history persist; hist seeds the input history; resumed is the number of
// restored conversation turns (0 = fresh).
func New(ag *agent.Agent, router *llm.Router, toolStat map[string]string, sessionPath string, hist []string, resumed int, askCh chan AskReq, proxyMgr *proxy.Manager, cfg *config.Config, cfgPath string) *tea.Program {
	ti := textinput.New()
	ti.Placeholder = "instruction or /command (/model /clear /doctor /help) · Enter run · ↑/↓ history"
	ti.Focus()
	ti.CharLimit = 0
	ti.Prompt = userStyle.Render("jurig› ")

	ai := textinput.New()
	ai.CharLimit = 0
	ai.Prompt = neonStyle.Render("answer› ")

	si := textinput.New()
	si.CharLimit = 0
	si.Prompt = neonStyle.Render("/")

	ki := textinput.New()
	ki.CharLimit = 0
	ki.EchoMode = textinput.EchoPassword
	ki.EchoCharacter = '•'
	ki.Prompt = neonStyle.Render("key› ")

	sp := spinner.New()
	sp.Spinner = spinner.Points
	sp.Style = neonStyle

	m := &model{
		ag: ag, router: router, toolStat: toolStat,
		sessionPath: sessionPath, resumed: resumed,
		hist: hist, histIdx: len(hist),
		ti: ti, askIn: ai, searchIn: si, keyInput: ki, spin: sp, status: "idle",
		askCh: askCh, proxy: proxyMgr, cfg: cfg, cfgPath: cfgPath,
	}
	return tea.NewProgram(m, tea.WithAltScreen())
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, m.spin.Tick, animTick(), m.waitAsk())
}

// waitAsk blocks for the next agent question.
func (m *model) waitAsk() tea.Cmd {
	ch := m.askCh
	return func() tea.Msg { return askMsg(<-ch) }
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		if !m.ready {
			m.vp = viewport.New(msg.Width-2, msg.Height-10)
			m.vp.SetContent(m.banner())
			m.ready = true
		}
		m.relayout()
		m.ti.Width = msg.Width - 12

	case tea.KeyMsg:
		if m.picking {
			return m, m.updatePicker(msg)
		}
		if m.keyPrompt {
			return m, m.handleKeyPrompt(msg)
		}
		if m.asking {
			return m, m.updateAsk(msg)
		}
		if m.searching {
			return m, m.updateSearch(msg)
		}
		switch msg.String() {
		case "ctrl+c":
			if m.cancel != nil {
				m.cancel()
			}
			return m, tea.Quit
		case "esc":
			// Interrupt the running agent so you can steer/correct it.
			if m.running && m.cancel != nil {
				m.cancel()
				m.appendRaw(toolStyle.Render("⏸ interrupted — type a correction to steer the agent") + "\n")
			}
			return m, nil
		case "ctrl+o":
			m.openPicker()
			return m, nil
		case "ctrl+f":
			m.searching = true
			m.searchIn.Reset()
			m.searchIn.Focus()
			m.lastQuery = ""
			return m, textinput.Blink
		case "f1", "ctrl+h":
			m.helpOn = !m.helpOn
			return m, nil
		case "enter":
			if !m.running && strings.TrimSpace(m.ti.Value()) != "" {
				input := strings.TrimSpace(m.ti.Value())
				m.ti.Reset()
				if strings.HasPrefix(input, "/") {
					cmds = append(cmds, m.execSlash(input))
				} else {
					m.hist = append(m.hist, input)
					m.histIdx = len(m.hist)
					m.appendRaw(userStyle.Render("▸ you: ") + m.wrapText(input) + "\n\n")
					cmds = append(cmds, m.startRun(input))
				}
			}
		case "up":
			if !m.running {
				m.histPrev()
			}
		case "down":
			if !m.running {
				m.histNext()
			}
		case "pgup":
			m.vp.HalfViewUp()
		case "pgdown":
			m.vp.HalfViewDown()
		}

	case askMsg:
		m.asking = true
		m.askReq = AskReq(msg)
		m.askCursor = 0
		m.askSel = map[int]bool{}
		m.appendRaw("\n" + neonStyle.Render("？ agent asks:") + " " + m.wrapText(m.askReq.Question) + "\n")
		m.askIn.Reset()
		if len(m.askReq.Options) == 0 {
			m.askIn.Focus() // free-text answer
		}
		return m, textinput.Blink

	case animMsg:
		m.frame++
		// proxy may start/stop mid-run → re-check the split layout each tick
		if m.ready && m.panelOn != m.wantPanel() {
			m.relayout()
		}
		cmds = append(cmds, animTick())

	case spinner.TickMsg:
		var sc tea.Cmd
		m.spin, sc = m.spin.Update(msg)
		cmds = append(cmds, sc)

	case eventMsg:
		m.handleEvent(agent.Event(msg))
		cmds = append(cmds, m.waitEvent())

	case runDoneMsg:
		m.running = false
		m.curOp = ""
		switch {
		case msg.err != nil && isCanceled(msg.err):
			m.appendRaw(toolStyle.Render("⏸ stopped — steer with a new message") + "\n\n")
		case msg.err != nil:
			m.appendRaw(errStyle.Render("✗ run ended: "+msg.err.Error()) + "\n\n")
		default:
			m.appendRaw(neonStyle.Render("■ done") + "\n\n")
		}
		m.status = "idle"
		// persist conversation + prompt history so the session can resume
		if m.sessionPath != "" {
			_ = m.ag.SaveSession(m.sessionPath, m.hist)
		}
	}

	var cmd tea.Cmd
	switch {
	case m.asking:
		m.askIn, cmd = m.askIn.Update(msg)
	case m.keyPrompt:
		m.keyInput, cmd = m.keyInput.Update(msg)
	case m.searching:
		m.searchIn, cmd = m.searchIn.Update(msg)
	default:
		m.ti, cmd = m.ti.Update(msg)
	}
	cmds = append(cmds, cmd)
	m.vp, cmd = m.vp.Update(msg)
	cmds = append(cmds, cmd)
	return m, tea.Batch(cmds...)
}

// updateSearch handles transcript search: Enter finds next match, Esc closes.
func (m *model) updateSearch(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.searching = false
		m.searchIn.Blur()
		return nil
	case "enter":
		q := strings.TrimSpace(m.searchIn.Value())
		if q == "" {
			return nil
		}
		if q != m.lastQuery {
			m.matches = m.findMatches(q)
			m.matchCur = 0
			m.lastQuery = q
		} else if len(m.matches) > 0 {
			m.matchCur = (m.matchCur + 1) % len(m.matches)
		}
		if len(m.matches) > 0 {
			m.vp.SetYOffset(m.matches[m.matchCur])
		}
		return nil
	}
	var cmd tea.Cmd
	m.searchIn, cmd = m.searchIn.Update(msg)
	return cmd
}

// findMatches returns transcript line indices containing q (case-insensitive,
// ANSI-stripped).
func (m *model) findMatches(q string) []int {
	q = strings.ToLower(q)
	var out []int
	for i, ln := range strings.Split(m.buf.String(), "\n") {
		if strings.Contains(strings.ToLower(stripANSI(ln)), q) {
			out = append(out, i)
		}
	}
	return out
}

// stripANSI removes SGR escape sequences so search matches visible text.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			for i += 2; i < len(s) && s[i] != 'm'; i++ {
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// updateAsk handles the operator answering an agent question. With options it
// is an arrow-key, multi-select checkbox list; with none it is a text prompt.
func (m *model) updateAsk(msg tea.KeyMsg) tea.Cmd {
	if msg.String() == "ctrl+c" {
		return tea.Quit
	}
	opts := m.askReq.Options

	// No options → free-text answer.
	if len(opts) == 0 {
		switch msg.String() {
		case "enter":
			if v := strings.TrimSpace(m.askIn.Value()); v != "" {
				return m.answer(v)
			}
			return nil
		}
		var cmd tea.Cmd
		m.askIn, cmd = m.askIn.Update(msg)
		return cmd
	}

	// Options → checkbox list.
	switch msg.String() {
	case "up", "ctrl+p", "k":
		if m.askCursor > 0 {
			m.askCursor--
		}
	case "down", "ctrl+n", "j", "tab":
		if m.askCursor < len(opts)-1 {
			m.askCursor++
		}
	case " ", "x":
		m.askSel[m.askCursor] = !m.askSel[m.askCursor]
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		idx := int(msg.String()[0] - '1')
		if idx < len(opts) {
			m.askSel[idx] = !m.askSel[idx]
			m.askCursor = idx
		}
	case "enter":
		var picked []string
		for i, o := range opts {
			if m.askSel[i] {
				picked = append(picked, o)
			}
		}
		if len(picked) == 0 {
			picked = []string{opts[m.askCursor]} // none toggled → current row
		}
		return m.answer(strings.Join(picked, "; "))
	}
	return nil
}

// helpView lists keybindings (toggled with F1).
func (m *model) helpView() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(" SLASH COMMANDS ") + "\n\n")
	for _, c := range slashCommands {
		b.WriteString("  " + neonStyle.Render(fmt.Sprintf("%-10s", c.cmd)) + statusStyle.Render(c.desc) + "\n")
	}
	b.WriteString("\n" + titleStyle.Render(" KEYBINDINGS ") + "\n\n")
	rows := [][2]string{
		{"Enter", "run instruction / submit"},
		{"↑ / ↓", "prompt history"},
		{"PgUp / PgDn", "scroll transcript"},
		{"Ctrl+F", "search transcript (Enter=next)"},
		{"Ctrl+O", "switch provider / model"},
		{"Esc", "interrupt the running agent (then steer it)"},
		{"F1", "toggle this help"},
		{"Ctrl+C", "quit"},
	}
	for _, r := range rows {
		b.WriteString("  " + neonStyle.Render(fmt.Sprintf("%-10s", r[0])) + statusStyle.Render(r[1]) + "\n")
	}
	b.WriteString("\n" + statusStyle.Render("press F1 or /help to close"))
	return b.String()
}

// askMenu renders the multi-select checkbox list (body overlay, no border —
// the body area supplies the border).
func (m *model) askMenu() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(" ？ ANSWER THE AGENT ") + "\n")
	b.WriteString(neonStyle.Render(wrap.String(m.askReq.Question, m.innerWidth()-2)) + "\n\n")
	for i, o := range m.askReq.Options {
		box := statusStyle.Render("[ ]")
		if m.askSel[i] {
			box = neonStyle.Render("[x]")
		}
		label := fmt.Sprintf("%d) %s", i+1, o)
		var line string
		if i == m.askCursor {
			line = selStyle.Render("› ") + box + " " + selStyle.Render(label)
		} else {
			line = "  " + box + " " + label
		}
		b.WriteString(wrap.String(line, m.innerWidth()-2) + "\n")
	}
	return b.String()
}

// answer delivers the operator's reply back to the waiting agent goroutine.
func (m *model) answer(ans string) tea.Cmd {
	m.appendRaw(neonStyle.Render("↳ you: ") + m.wrapText(ans) + "\n\n")
	if m.askReq.Reply != nil {
		m.askReq.Reply <- ans
	}
	m.asking = false
	m.askIn.Blur()
	return m.waitAsk() // arm for the next question
}

func (m *model) View() string {
	if !m.ready {
		return "starting jurig…"
	}
	// Persistent animated ghost header (stays pinned; transcript scrolls below).
	logo := m.renderLogo()
	head := logo + "\n" +
		statusStyle.Render(fmt.Sprintf("  %s · %s%s · F1 help", m.router.ActiveLabel(), m.status, m.tokenTag()))

	// Overlays render in the fixed-height body area so tall menus never break
	// the layout; the footer stays a single hint/input line.
	inner := m.vp.View()
	switch {
	case m.helpOn:
		inner = m.helpView()
	case m.keyPrompt:
		inner = m.keyPromptView()
	case m.asking && len(m.askReq.Options) > 0:
		inner = m.askMenu()
	case m.picking:
		inner = m.pickerMenu()
	}
	bodyW := m.w - 2
	if m.panelOn {
		bodyW = m.w - m.panelW - 3
	}
	body := borderStyle.Width(bodyW).Height(m.vp.Height).Render(inner)
	if m.panelOn {
		body = lipgloss.JoinHorizontal(lipgloss.Top, body, " ", m.proxyPanel(m.panelW, m.vp.Height))
	}

	var foot string
	switch {
	case m.keyPrompt:
		foot = neonStyle.Render("key› ") + statusStyle.Render("paste API key · Enter save · Esc cancel")
	case m.searching:
		hint := "  Enter search · Esc close"
		if m.lastQuery != "" {
			hint = fmt.Sprintf("  %d/%d · Enter next · Esc close", m.matchCur+1, len(m.matches))
		}
		foot = m.searchIn.View() + statusStyle.Render(hint)
	case m.asking && len(m.askReq.Options) == 0:
		foot = m.askIn.View() + statusStyle.Render("  type answer · Enter")
	case m.asking:
		foot = neonStyle.Render("answer↑ ") + statusStyle.Render("↑/↓ move · Space or 1-9 toggle (multi) · Enter confirm")
	case m.picking:
		foot = statusStyle.Render("↑/↓ select · Enter apply · Esc cancel")
	case m.running:
		op := m.curOp
		if op == "" {
			op = "thinking"
		}
		foot = m.spin.View() + " " + neonStyle.Render("["+op+"]") + " " +
			statusStyle.Render(m.status+"  ·  Esc interrupt · Ctrl+C quit")
	default:
		foot = m.ti.View()
	}
	return head + "\n" + body + "\n" + foot
}

// wantPanel reports whether the proxy side-panel should be shown (proxy live
// AND the terminal is wide enough).
func (m *model) wantPanel() bool {
	return m.proxy != nil && m.proxy.Running() && m.w >= 100
}

// relayout recomputes widths for the responsive transcript / proxy-panel split.
func (m *model) relayout() {
	if !m.ready {
		return
	}
	m.panelOn = m.wantPanel()
	m.panelW = 0
	bodyW := m.w - 2
	if m.panelOn {
		m.panelW = m.w / 3
		if m.panelW > 52 {
			m.panelW = 52
		}
		if m.panelW < 32 {
			m.panelW = 32
		}
		bodyW = m.w - m.panelW - 3
	}
	vpH := m.h - 10
	if vpH < 3 {
		vpH = 3
	}
	m.vp.Width = bodyW - 2
	m.vp.Height = vpH
	// rebuild the markdown renderer only when its wrap width actually changes
	if gw := bodyW - 6; gw != m.glamW && gw > 10 {
		m.glam, _ = glamour.NewTermRenderer(glamour.WithAutoStyle(), glamour.WithWordWrap(gw))
		m.glamW = gw
	}
}

// proxyPanel renders the live MITM capture side-panel.
func (m *model) proxyPanel(w, h int) string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(" ☰ NET ") + statusStyle.Render(fmt.Sprintf(" %d flows", m.proxy.Count())) + "\n\n")
	flows := m.proxy.Recent(h - 3)
	if len(flows) == 0 {
		b.WriteString(statusStyle.Render("waiting for traffic…\nset device proxy +\ninstall CA + frida unpin"))
	}
	for _, f := range flows {
		st := statusStyle
		switch {
		case f.Status >= 500:
			st = errStyle
		case f.Status >= 400:
			st = toolStyle
		case f.Status >= 200 && f.Status < 300:
			st = cmdStyle
		}
		line := fmt.Sprintf("%s %s%s", f.Method, f.Host, f.Path)
		b.WriteString(st.Render(fmt.Sprintf("%3d", f.Status)) + " " + wrap.String(line, w-6) + "\n")
	}
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cNeon).
		Width(w - 2).Height(h)
	return box.Render(b.String())
}

// ---- slash commands (Claude Code style) ----

var slashCommands = []struct {
	cmd  string
	args string
	desc string
}{
	{"/model", "", "switch provider + model (opens picker)"},
	{"/m", "", "alias for /model"},
	{"/clear", "", "clear transcript"},
	{"/doctor", "", "show provider + toolchain status"},
	{"/session", "", "show session info (turns, tokens)"},
	{"/fresh", "", "start a fresh session (discard history)"},
	{"/help", "", "toggle help overlay (same as F1)"},
	{"/quit", "", "quit jurig"},
}

// execSlash handles a `/command` input. Returns a tea.Cmd if needed.
func (m *model) execSlash(input string) tea.Cmd {
	parts := strings.Fields(input)
	cmd := strings.ToLower(parts[0])

	switch cmd {
	case "/model", "/m":
		m.openPicker()
		return nil

	case "/clear":
		m.buf.Reset()
		m.vp.SetContent(m.banner())
		m.vp.GotoTop()
		return nil

	case "/doctor":
		sel := m.router.ActiveLabel()
		prov := m.router.ProviderName()
		var b strings.Builder
		b.WriteString(titleStyle.Render(" DOCTOR ") + "\n\n")
		b.WriteString("  active:   " + neonStyle.Render(sel) + "\n")
		b.WriteString("  provider: " + statusStyle.Render(prov) + "\n")
		b.WriteString("  tools:\n")
		for name, path := range m.toolStat {
			mark := cmdStyle.Render("✓")
			if path == "MISSING" {
				mark = errStyle.Render("✗")
			}
			b.WriteString(fmt.Sprintf("    %s %-9s %s\n", mark, name, statusStyle.Render(path)))
		}
		if m.tokIn+m.tokOut > 0 {
			b.WriteString(fmt.Sprintf("\n  tokens:   %dk in / %dk out\n", m.tokIn/1000, m.tokOut/1000))
		}
		b.WriteString("\n")
		m.appendRaw(b.String())
		return nil

	case "/session":
		turns := m.ag.Turns()
		var b strings.Builder
		b.WriteString(titleStyle.Render(" SESSION ") + "\n\n")
		b.WriteString(fmt.Sprintf("  turns:    %d\n", turns))
		b.WriteString(fmt.Sprintf("  prompts:  %d\n", len(m.hist)))
		if m.tokIn+m.tokOut > 0 {
			b.WriteString(fmt.Sprintf("  tokens:   %dk in / %dk out\n", m.tokIn/1000, m.tokOut/1000))
		}
		if m.sessionPath != "" {
			b.WriteString("  saved:    " + statusStyle.Render(m.sessionPath) + "\n")
		}
		b.WriteString("\n")
		m.appendRaw(b.String())
		return nil

	case "/fresh":
		m.ag.Reset()
		m.buf.Reset()
		m.hist = nil
		m.histIdx = 0
		m.tokIn, m.tokOut = 0, 0
		m.resumed = 0
		m.vp.SetContent(m.banner())
		m.vp.GotoTop()
		m.appendRaw(neonStyle.Render("↻ fresh session") + "\n\n")
		return nil

	case "/help":
		m.helpOn = !m.helpOn
		return nil

	case "/quit":
		return tea.Quit

	default:
		// unknown slash command — show hint
		m.appendRaw(errStyle.Render("unknown command: "+cmd) + "\n")
		m.appendRaw(statusStyle.Render("available: ") + neonStyle.Render("/model /clear /doctor /session /fresh /help /quit") + "\n\n")
		return nil
	}
}

// tokenTag shows cumulative token usage in the header when available.
func (m *model) tokenTag() string {
	if m.tokIn+m.tokOut == 0 {
		return ""
	}
	return fmt.Sprintf(" · %dk↑ %dk↓", m.tokIn/1000, m.tokOut/1000)
}

// ---- model picker ----

func (m *model) openPicker() {
	m.choices = m.router.Catalog()
	m.picking = true
	m.pcursor = 0
	cur := m.router.ActiveLabel()
	for i, c := range m.choices {
		if c.Provider+"/"+c.Model == cur {
			m.pcursor = i
			break
		}
	}
}

func (m *model) updatePicker(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "ctrl+o":
		m.picking = false
	case "up", "ctrl+p":
		if m.pcursor > 0 {
			m.pcursor--
		}
	case "down", "ctrl+n":
		if m.pcursor < len(m.choices)-1 {
			m.pcursor++
		}
	case "enter":
		if m.pcursor < len(m.choices) {
			c := m.choices[m.pcursor]
			if !c.Ready {
				// Provider needs a key — prompt for it inline
				m.picking = false
				m.startKeyPrompt(c.Provider, c.Model)
				return nil
			}
			if err := m.router.SetSelection(c.Provider, c.Model); err == nil {
				m.appendRaw(statusStyle.Render("→ model: "+c.Provider+"/"+c.Model) + "\n")
			}
		}
		m.picking = false
	}
	return nil
}

func (m *model) pickerMenu() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(" SELECT MODEL ") + statusStyle.Render("  ↑/↓ · Enter apply · Esc cancel") + "\n\n")
	for i, c := range m.choices {
		line := c.Provider + "/" + c.Model
		if i == m.pcursor {
			b.WriteString(selStyle.Render("› "+line) + "\n")
			continue
		}
		if c.Ready {
			b.WriteString("  " + line + "\n")
		} else {
			b.WriteString(statusStyle.Render("  "+line+" (no key)") + "\n")
		}
	}
	return b.String()
}

// ---- key prompt (inline API key entry) ----

func (m *model) startKeyPrompt(prov, model string) {
	m.keyPrompt = true
	m.keyForProv = prov
	m.keyForModel = model
	m.keyInput.Reset()
	m.keyInput.Focus()
}

// handleKeyPrompt processes keys while the key input is active.
func (m *model) handleKeyPrompt(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.keyPrompt = false
		m.keyInput.Blur()
		m.appendRaw(statusStyle.Render("cancelled") + "\n")
		return nil
	case "enter":
		key := strings.TrimSpace(m.keyInput.Value())
		if key == "" {
			return nil
		}
		m.keyPrompt = false
		m.keyInput.Blur()
		return m.saveKeyAndSelect(key)
	}
	var cmd tea.Cmd
	m.keyInput, cmd = m.keyInput.Update(msg)
	return cmd
}

// saveKeyAndSelect persists the key to config, reloads the router, and selects
// the model.
func (m *model) saveKeyAndSelect(key string) tea.Cmd {
	prov := m.keyForProv
	mod := m.keyForModel

	// Update the config struct in memory
	if pc, ok := m.cfg.Providers[prov]; ok {
		pc.APIKey = key
		m.cfg.Providers[prov] = pc
	}

	// Save config.json so the key persists across restarts
	if m.cfgPath != "" {
		if err := m.cfg.Save(m.cfgPath); err != nil {
			m.appendRaw(errStyle.Render("save config: "+err.Error()) + "\n")
		}
	}

	// Rebuild the router so the new key takes effect
	if newRouter, err := llm.NewRouter(m.cfg); err == nil {
		// Update providers + readiness without copying the mutex
		m.router.ReplaceProviders(newRouter)
	}

	// Now select the model
	if err := m.router.SetSelection(prov, mod); err == nil {
		m.appendRaw(cmdStyle.Render("✓ key saved") + " " + neonStyle.Render("→ "+prov+"/"+mod) + "\n\n")
	} else {
		m.appendRaw(errStyle.Render("select failed: "+err.Error()) + "\n")
	}
	return nil
}

func (m *model) keyPromptView() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(" SET API KEY ") + "\n\n")
	b.WriteString("  provider: " + neonStyle.Render(m.keyForProv) + "\n")
	b.WriteString("  model:    " + neonStyle.Render(m.keyForModel) + "\n\n")
	b.WriteString("  " + m.keyInput.View() + "\n\n")
	b.WriteString(statusStyle.Render("  paste key + Enter to save · Esc cancel"))
	return borderStyle.Width(m.innerWidth()).Render(b.String())
}

// ---- agent worker plumbing ----

func (m *model) startRun(task string) tea.Cmd {
	m.running = true
	m.status = "running"
	m.events = make(chan agent.Event, 64)
	m.done = make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	go func() {
		_, err := m.ag.Run(ctx, task, func(e agent.Event) { m.events <- e })
		m.done <- err
		close(m.events)
	}()
	return m.waitEvent()
}

func (m *model) waitEvent() tea.Cmd {
	ch, done := m.events, m.done
	return func() tea.Msg {
		e, ok := <-ch
		if !ok {
			select {
			case err := <-done:
				return runDoneMsg{err: err}
			default:
				return runDoneMsg{}
			}
		}
		return eventMsg(e)
	}
}

func (m *model) handleEvent(e agent.Event) {
	switch e.Kind {
	case agent.EvStatus:
		m.status = e.Text
	case agent.EvUsage:
		m.tokIn += e.In
		m.tokOut += e.Out
	case agent.EvText:
		m.appendMarkdown(e.Text)
	case agent.EvToolCall:
		m.curOp = e.Tool
		if e.Tool == "read_file" {
			m.lastRead = pathArg(e.Text)
		}
		prefix := glyph(e.Tool) + " " + e.Tool + " "
		arg := truncate(strings.ReplaceAll(sanitize(e.Text), "\n", " "), max(6, m.innerWidth()-lipgloss.Width(prefix)-1))
		m.appendRaw(toolStyle.Render(prefix) + statusStyle.Render(arg) + "\n")
	case agent.EvToolResult:
		m.curOp = ""
		switch {
		case e.Tool == "read_file" && highlight.IsCode(m.lastRead, e.Text):
			// AI read a source file → show a formatted, highlighted code card.
			m.appendCodeCard(m.lastRead, e.Text)
		case isReport(e.Text):
			m.appendMarkdown(e.Text) // jadx/apktool markdown summaries
		default:
			m.appendRaw(cmdStyle.Render("  ┗━ ") + statusStyle.Render(fmt.Sprintf("%d bytes", len(e.Text))) + "\n")
			m.appendRaw(m.dimBlock(truncate(sanitize(e.Text), 1600)) + "\n")
		}
	case agent.EvCmd:
		m.appendRaw(m.prefixBlock("  ▸ ", cmdStyle, sanitize(e.Text)))
	case agent.EvError:
		m.appendRaw(m.prefixBlock("✗ ", errStyle, sanitize(e.Text)))
	}
}

// isCanceled reports whether an error came from an ESC interrupt.
func isCanceled(err error) bool {
	s := err.Error()
	return strings.Contains(s, "context canceled") || strings.Contains(s, "canceled")
}

// sanitize strips carriage returns and other C0 control bytes from tool output
// so they can't corrupt the TUI (Windows adb/shell emits CRLF).
func sanitize(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "")
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f {
			return -1 // drop other control chars
		}
		return r
	}, s)
}

// prefixBlock wraps text to the content width MINUS the prefix, then prefixes
// the first visual line and indents continuations — so styled prefixes can
// never push a line past the border.
func (m *model) prefixBlock(prefix string, style lipgloss.Style, text string) string {
	w := m.innerWidth() - lipgloss.Width(prefix)
	if w < 10 {
		w = 10
	}
	pad := strings.Repeat(" ", lipgloss.Width(prefix))
	var b strings.Builder
	first := true
	for _, logical := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		for _, vis := range strings.Split(wrap.String(logical, w), "\n") {
			if first {
				b.WriteString(neonStyle.Render(prefix) + style.Render(vis) + "\n")
				first = false
			} else {
				b.WriteString(pad + style.Render(vis) + "\n")
			}
		}
	}
	return b.String()
}

// previewLines caps how many code lines the TUI card shows. The AI still
// receives the FULL file via the tool result — this card is display-only.
const previewLines = 12

// appendCodeCard renders a small, syntax-highlighted preview whenever the AI
// reads a source file. Trimmed for the eyes; the model has full context.
func (m *model) appendCodeCard(path, code string) {
	w := m.innerWidth()
	if w > 90 {
		w = 90 // keep the card compact even on wide terminals
	}
	total := strings.Count(strings.TrimRight(code, "\n"), "\n") + 1
	base := filepath.Base(path)
	lang := highlight.Lang(path)
	title := fmt.Sprintf(" %s %s ", glyph("read_file"), base)
	note := fmt.Sprintf(" %d/%d·%s ", min(previewLines, total), total, lang)
	rule := strings.Repeat("─", max(0, w-lipgloss.Width(title)-len(note)-3))
	top := neonStyle.Render("╭─"+title) + statusStyle.Render(rule+note+"╮")
	body := highlight.Code(code, path, w-2, previewLines, 0)
	bot := statusStyle.Render("╰" + strings.Repeat("─", max(0, w-2)) + "╯")

	var b strings.Builder
	b.WriteString(top + "\n")
	for _, ln := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
		b.WriteString(statusStyle.Render("│ ") + ln + "\n")
	}
	b.WriteString(bot + "\n")
	m.appendRaw(b.String())
}

// wrapText hard-wraps text (ANSI-aware) to the inner content width.
func (m *model) wrapText(s string) string {
	w := m.innerWidth()
	if w <= 0 {
		return s
	}
	return wrap.String(s, w)
}

func (m *model) innerWidth() int {
	if m.vp.Width > 2 {
		return m.vp.Width - 2
	}
	if m.w > 4 {
		return m.w - 4
	}
	return 76
}

// histPrev/histNext cycle the input through submitted-prompt history.
func (m *model) histPrev() {
	if len(m.hist) == 0 {
		return
	}
	if m.histIdx > 0 {
		m.histIdx--
	}
	m.ti.SetValue(m.hist[m.histIdx])
	m.ti.CursorEnd()
}
func (m *model) histNext() {
	if len(m.hist) == 0 {
		return
	}
	if m.histIdx < len(m.hist)-1 {
		m.histIdx++
		m.ti.SetValue(m.hist[m.histIdx])
	} else {
		m.histIdx = len(m.hist)
		m.ti.SetValue("")
	}
	m.ti.CursorEnd()
}

// pathArg pulls "path" out of a read_file tool-call JSON blob.
func pathArg(raw string) string {
	var v struct {
		Path string `json:"path"`
	}
	if json.Unmarshal([]byte(raw), &v) == nil {
		return v.Path
	}
	return ""
}

// isReport detects tool output that is already Markdown (decompile summaries).
func isReport(s string) bool {
	return strings.HasPrefix(strings.TrimSpace(s), "## ")
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (m *model) appendMarkdown(md string) {
	rendered := md
	if m.glam != nil {
		if out, err := m.glam.Render(md); err == nil {
			rendered = out
		}
	}
	m.appendRaw(rendered)
}

func (m *model) appendRaw(s string) {
	m.buf.WriteString(s)
	m.vp.SetContent(m.buf.String())
	m.vp.GotoBottom()
}

// jurigArt is the wordmark, joined horizontally with an animated ghost.
const jurigArt = `     ██ ██    ██ ██████  ██  ██████
     ██ ██    ██ ██   ██ ██ ██
     ██ ██    ██ ██████  ██ ██   ███
██   ██ ██    ██ ██   ██ ██ ██    ██
 █████   ██████  ██   ██ ██  ██████`

// ghostFrames animate the mascot: eyes blink, bob, and glance around.
// Each frame is a clean 5-wide × 5-line box.
var ghostFrames = []string{
	" .-. \n(o o)\n| u |\n|   |\n'~-~'",
	" .-. \n(o o)\n| u |\n|   |\n~'-'~",
	" .-. \n( oo)\n| u |\n|   |\n'~-~'",
	" .-. \n(- -)\n| u |\n|   |\n'~-~'",
	" .-. \n(o o)\n| u |\n|   |\n'~-~'",
	" .-. \n(oo )\n| u |\n|   |\n~'-'~",
}

var shimmer = []lipgloss.Color{"#00FF9C", "#3BFFB2", "#6BFFC8", "#9BFFDD", "#6BFFC8", "#3BFFB2"}

// animMsg advances the header animation.
type animMsg struct{}

func animTick() tea.Cmd {
	return tea.Tick(140*time.Millisecond, func(time.Time) tea.Msg { return animMsg{} })
}

// renderLogo joins the animated ghost with the wordmark and applies a moving
// color shimmer based on the current frame.
func (m *model) renderLogo() string {
	ghost := ghostFrames[(m.frame/2)%len(ghostFrames)]
	logo := lipgloss.JoinHorizontal(lipgloss.Center, ghost, "   ", jurigArt)
	return colorizeShimmer(logo, m.frame)
}

// colorizeShimmer colors each non-space rune with a palette entry offset by the
// frame, producing a wave that travels across the art.
func colorizeShimmer(s string, frame int) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		col := 0
		for _, r := range line {
			if r == ' ' {
				b.WriteRune(' ')
				continue
			}
			c := shimmer[(col+frame)%len(shimmer)]
			b.WriteString(lipgloss.NewStyle().Foreground(c).Render(string(r)))
			col++
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m *model) banner() string {
	var b strings.Builder
	b.WriteString(statusStyle.Render("autonomous reverse-engineering agent · android · binary · frida") + "\n\n")
	b.WriteString(statusStyle.Render("model: "+m.router.ActiveLabel()) + statusStyle.Render("   (Ctrl+O to switch)") + "\n")
	if m.resumed > 0 {
		b.WriteString(neonStyle.Render(fmt.Sprintf("↻ resumed session — %d prior turns loaded", m.resumed)) + "\n")
	}
	b.WriteString(statusStyle.Render("toolchain:") + "\n")
	for name, path := range m.toolStat {
		mark := cmdStyle.Render("✓")
		if path == "MISSING" {
			mark = errStyle.Render("✗")
		}
		b.WriteString(fmt.Sprintf("  %s %-9s %s\n", mark, name, statusStyle.Render(path)))
	}
	b.WriteString("\n" + statusStyle.Render("Type a target or instruction below and press Enter.") + "\n")
	return b.String()
}

// dimBlock renders tool output as a dim, border-quoted block, wrapping each
// line to the content width (minus the "  │ " gutter) so it never overflows.
func (m *model) dimBlock(s string) string {
	w := m.innerWidth() - 4
	if w < 10 {
		w = 10
	}
	var out []string
	for _, logical := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		for _, vis := range strings.Split(wrap.String(logical, w), "\n") {
			out = append(out, statusStyle.Render("  │ "+vis))
		}
	}
	return strings.Join(out, "\n")
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
