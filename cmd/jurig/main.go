// Command jurig is a fully autonomous reverse-engineering agent with a TUI.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/imtaqin/jurig/internal/agent"
	"github.com/imtaqin/jurig/internal/config"
	"github.com/imtaqin/jurig/internal/cursor"
	"github.com/imtaqin/jurig/internal/llm"
	"github.com/imtaqin/jurig/internal/portable"
	"github.com/imtaqin/jurig/internal/proxy"
	"github.com/imtaqin/jurig/internal/tools"
	"github.com/imtaqin/jurig/internal/tui"
)

func main() {
	var (
		cfgPath  = flag.String("config", config.DefaultPath(), "path to config.json")
		target   = flag.String("target", "", "work dir for this session (default <work_dir>/session)")
		printReq = flag.String("p", "", "headless: run this task, stream to stdout, exit")
		fresh    = flag.Bool("fresh", false, "ignore any saved session and start clean")
	)
	flag.Parse()

	// Subcommands: install <tool>, doctor, setup.
	forceSetup := false
	if args := flag.Args(); len(args) > 0 {
		switch args[0] {
		case "install":
			os.Exit(cmdInstall(*cfgPath, args[1:]))
		case "doctor":
			os.Exit(cmdDoctor(*cfgPath))
		case "setup":
			forceSetup = true
		case "cursor":
			os.Exit(cmdCursor(args[1:]))
		}
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		die("config: %v", err)
	}

	// First-run / not-ready → interactive setup wizard (unless headless).
	if *printReq == "" && (forceSetup || !config.Exists(*cfgPath) || !cfg.ActiveReady()) {
		ok, werr := tui.RunWizard(cfg, *cfgPath)
		if werr != nil {
			die("setup: %v", werr)
		}
		if !ok {
			die("setup cancelled — nothing saved")
		}
		if cfg, err = config.Load(*cfgPath); err != nil {
			die("config reload: %v", err)
		}
	}

	router, err := llm.NewRouter(cfg)
	if err != nil {
		die("llm: %v\n\nrun `jurig setup` to reconfigure", err)
	}

	pm := portable.New(cfg.ToolsDir)
	workDir := *target
	if workDir == "" {
		workDir = filepath.Join(cfg.WorkDir, "session")
	}
	_ = os.MkdirAll(workDir, 0o755)

	proxyMgr := proxy.New(filepath.Join(workDir, "proxy"))
	env := &tools.Env{WorkDir: workDir, Proxy: proxyMgr}
	// Contextual toolchain: when the agent calls a tool whose binary is
	// missing but auto-installable, fetch it on demand and stream progress.
	env.ResolveBin = func(name string) (string, error) {
		return pm.ResolveOrInstall(name, func(s string) {
			if env.Emit != nil {
				env.Emit("cmd", s)
			}
		})
	}
	reg := tools.NewRegistry()
	ag := agent.New(cfg, router, reg, env)

	if *printReq != "" {
		// Headless: no interactive user — answer scope questions with a default.
		env.Ask = func(q string, _ []string) string {
			return "No interactive user (headless). Proceed autonomously: prefer static analysis; only use dynamic tools if a device is clearly available."
		}
		os.Exit(runHeadless(ag, *printReq))
	}

	// Interactive session: resume prior conversation + prompt history.
	sessionPath := filepath.Join(workDir, "session.json")
	var histInit []string
	resumed := 0
	if !*fresh {
		if s, ok := agent.LoadSession(sessionPath); ok {
			ag.Restore(s.History)
			histInit = s.Prompts
			resumed = len(s.History)
		}
	}

	// Bridge agent → TUI for interactive ask_user questions.
	askCh := make(chan tui.AskReq)
	env.Ask = func(q string, opts []string) string {
		r := tui.AskReq{Question: q, Options: opts, Reply: make(chan string, 1)}
		askCh <- r
		return <-r.Reply
	}

	prog := tui.New(ag, router, pm.Status(), sessionPath, histInit, resumed, askCh, proxyMgr)
	if _, err := prog.Run(); err != nil {
		die("tui: %v", err)
	}
}

// runHeadless streams agent events to stdout (no TUI).
func runHeadless(ag *agent.Agent, task string) int {
	_, err := ag.Run(context.Background(), task, func(e agent.Event) {
		switch e.Kind {
		case agent.EvStatus:
			fmt.Fprintln(os.Stderr, "· "+e.Text)
		case agent.EvCmd:
			fmt.Fprintln(os.Stderr, "$ "+e.Text)
		case agent.EvToolCall:
			fmt.Fprintf(os.Stderr, "⚙ %s %s\n", e.Tool, e.Text)
		case agent.EvToolResult:
			fmt.Fprintf(os.Stderr, "  ↳ %.400s\n", strings.TrimSpace(e.Text))
		case agent.EvText:
			fmt.Println(e.Text)
		case agent.EvError:
			fmt.Fprintln(os.Stderr, "✗ "+e.Text)
		}
	})
	if err != nil {
		return 1
	}
	return 0
}

func cmdInstall(cfgPath string, names []string) int {
	if len(names) == 0 {
		fmt.Println("usage: jurig install <tool>   (available:", catalogList(), ")")
		return 2
	}
	cfg, _ := config.Load(cfgPath)
	pm := portable.New(cfg.ToolsDir)
	for _, n := range names {
		fmt.Printf("installing %s → %s\n", n, cfg.ToolsDir)
		dir, err := pm.Install(n, func(s string) { fmt.Println("  " + s) })
		if err != nil {
			fmt.Println("  error:", err)
			return 1
		}
		fmt.Println("  ok:", dir)
	}
	return 0
}

func cmdDoctor(cfgPath string) int {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		die("config: %v", err)
	}
	fmt.Println("jurig doctor")
	fmt.Printf("  active   : %s / %s\n", cfg.Active.Provider, cfg.Active.Model)
	fmt.Println("  tools_dir:", cfg.ToolsDir)
	fmt.Println("  work_dir :", cfg.WorkDir)
	fmt.Println("  providers:")
	pnames := make([]string, 0, len(cfg.Providers))
	for n := range cfg.Providers {
		pnames = append(pnames, n)
	}
	sort.Strings(pnames)
	for _, n := range pnames {
		p := cfg.Providers[n]
		key := "no-key"
		if p.APIKey != "" {
			key = "key✓"
		}
		fmt.Printf("    %-11s %-9s %-6s %s\n", n, p.Kind, key, p.BaseURL)
	}
	fmt.Println("  toolchain:")
	pm := portable.New(cfg.ToolsDir)
	st := pm.Status()
	keys := make([]string, 0, len(st))
	for k := range st {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("    %-9s %s\n", k, st[k])
	}
	if _, err := llm.NewRouter(cfg); err != nil {
		fmt.Println("  llm      : NOT READY —", err)
		return 1
	}
	ap := cfg.Providers[cfg.Active.Provider]
	if ap.APIKey == "" && ap.Kind != config.KindClaudeCLI {
		fmt.Printf("  llm      : active provider %q has NO KEY — set %s or switch model (Ctrl+O)\n", cfg.Active.Provider, ap.KeyEnv)
		return 1
	}
	fmt.Println("  llm      : ready")
	return 0
}

// cmdCursor handles `jurig cursor login|status|logout` — native PKCE auth to a
// Cursor subscription.
func cmdCursor(args []string) int {
	sub := "status"
	if len(args) > 0 {
		sub = args[0]
	}
	path := cursor.DefaultStorePath()

	switch sub {
	case "login":
		p, err := cursor.GenerateAuthParams()
		if err != nil {
			fmt.Fprintln(os.Stderr, "cursor:", err)
			return 1
		}
		fmt.Println("Opening browser to log in to Cursor…")
		fmt.Println("If it doesn't open, visit:\n  " + p.LoginURL)
		openBrowser(p.LoginURL)
		fmt.Print("Waiting for login")
		creds, err := cursor.Poll(p.UUID, p.Verifier, func(int) { fmt.Print(".") })
		fmt.Println()
		if err != nil {
			fmt.Fprintln(os.Stderr, "cursor login:", err)
			return 1
		}
		if err := cursor.Save(path, creds); err != nil {
			fmt.Fprintln(os.Stderr, "save:", err)
			return 1
		}
		fmt.Println("✓ logged in — token saved to", path)
		return 0
	case "status":
		if !cursor.LoggedIn(path) {
			fmt.Println("cursor: not logged in (run `jurig cursor login`)")
			return 1
		}
		if _, err := cursor.ValidToken(path); err != nil {
			fmt.Println("cursor: token invalid —", err)
			return 1
		}
		fmt.Println("cursor: logged in, token valid")
		return 0
	case "logout":
		_ = os.Remove(path)
		fmt.Println("cursor: logged out")
		return 0
	case "token":
		// Print a valid access token (refreshing if needed) for a bridge to use.
		tok, err := cursor.ValidToken(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "cursor:", err)
			return 1
		}
		fmt.Println(tok)
		return 0
	case "serve":
		// Launch the cursor-openai-api bridge (OpenAI-compatible, port 3000).
		port := "3000"
		if len(args) > 1 {
			port = args[1]
		}
		fmt.Printf("Starting cursor-openai-api bridge on :%s → set the 'cursor' provider (Ctrl+O)\n", port)
		fmt.Println("(first time needs login: jurig cursor bridge login)")
		return runBridge("serve", port)
	case "bridge":
		// Passthrough to cursor-openai-api (login, whoami, models, …).
		return runBridge(args[1:]...)
	case "chat":
		// EXPERIMENTAL native Agent protocol test: jurig cursor chat <model> <prompt...>
		if len(args) < 3 {
			fmt.Println("usage: jurig cursor chat <model> <prompt>")
			return 2
		}
		tok, err := cursor.ValidToken(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "cursor:", err)
			return 1
		}
		model := args[1]
		prompt := strings.Join(args[2:], " ")
		fmt.Fprintln(os.Stderr, "[native cursor agent — experimental]")
		out, err := cursor.NewClient(tok).Chat(context.Background(), model, "You are a helpful assistant.", prompt)
		if out != "" {
			fmt.Println(out)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "cursor chat:", err)
			return 1
		}
		return 0
	default:
		fmt.Println("usage: jurig cursor login|status|token|logout|serve [port]|bridge <args>")
		fmt.Println("  login/status/token/logout : native Jurig auth (future no-bridge client)")
		fmt.Println("  serve [port]              : run the cursor-openai-api bridge (default 3000)")
		fmt.Println("  bridge login|whoami|...   : passthrough to cursor-openai-api")
		return 2
	}
}

// runBridge runs the cursor-openai-api npm package via bunx or npx, inheriting
// stdio so login prompts and the running server are visible.
func runBridge(args ...string) int {
	runner, runnerArgs := bridgeRunner()
	if runner == "" {
		fmt.Fprintln(os.Stderr, "cursor: need bun or node/npx installed to run the cursor-openai-api bridge")
		fmt.Fprintln(os.Stderr, "  install: https://github.com/shawtyygabriel/cursor-openai-api")
		return 1
	}
	full := append(append(runnerArgs, "cursor-openai-api"), args...)
	cmd := exec.Command(runner, full...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "cursor bridge:", err)
		return 1
	}
	return 0
}

// bridgeRunner picks an available package runner: bunx, then npx.
func bridgeRunner() (string, []string) {
	if p, err := exec.LookPath("bunx"); err == nil {
		return p, nil
	}
	if p, err := exec.LookPath("bun"); err == nil {
		return p, []string{"x"}
	}
	if p, err := exec.LookPath("npx"); err == nil {
		return p, []string{"-y"}
	}
	return "", nil
}

// openBrowser best-effort opens a URL in the default browser.
func openBrowser(u string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", u)
	case "darwin":
		cmd = exec.Command("open", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	_ = cmd.Start()
}

func catalogList() string {
	var ks []string
	for k := range portable.Catalog {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return strings.Join(ks, ", ")
}

func die(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "jurig: "+f+"\n", a...)
	os.Exit(1)
}
