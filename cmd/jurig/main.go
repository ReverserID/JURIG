// Command jurig is a fully autonomous reverse-engineering agent with a TUI.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/imtaqin/jurig/internal/agent"
	"github.com/imtaqin/jurig/internal/config"
	"github.com/imtaqin/jurig/internal/llm"
	"github.com/imtaqin/jurig/internal/portable"
	"github.com/imtaqin/jurig/internal/proxy"
	"github.com/imtaqin/jurig/internal/tools"
	"github.com/imtaqin/jurig/internal/tui"
)

// Version is the build version, overridable via -ldflags "-X main.Version=…".
var Version = "v2.0.0"

func main() {
	var (
		cfgPath  = flag.String("config", config.DefaultPath(), "path to config.json")
		target   = flag.String("target", "", "work dir for this session (default <work_dir>/session)")
		printReq = flag.String("p", "", "headless: run this task, stream to stdout, exit")
		fresh    = flag.Bool("fresh", false, "ignore any saved session and start clean")
		showVer  = flag.Bool("version", false, "print version and exit")
	)
	flag.Usage = usage
	flag.Parse()
	if *showVer {
		fmt.Println("jurig", Version)
		return
	}

	// Subcommands: install <tool>, doctor, setup, version, help.
	forceSetup := false
	if args := flag.Args(); len(args) > 0 {
		switch args[0] {
		case "install":
			os.Exit(cmdInstall(*cfgPath, args[1:]))
		case "doctor":
			os.Exit(cmdDoctor(*cfgPath))
		case "setup":
			forceSetup = true
		case "version":
			fmt.Println("jurig", Version)
			return
		case "help", "-h", "--help":
			usage()
			return
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

	// Headless: build once, run one task, exit.
	if *printReq != "" {
		router, err := llm.NewRouter(cfg)
		if err != nil {
			die("llm: %v\n\nrun `jurig setup` to reconfigure", err)
		}
		ag, env, _, _, _ := newSession(cfg, router, *target)
		env.Ask = func(q string, _ []string) string {
			return "No interactive user (headless). Proceed autonomously: prefer static analysis; only use dynamic tools if a device is clearly available."
		}
		os.Exit(runHeadless(ag, *printReq))
	}

	// Interactive: relaunch loop so /setup can re-run the wizard live.
	for {
		router, err := llm.NewRouter(cfg)
		if err != nil {
			die("llm: %v\n\nrun `jurig setup` to reconfigure", err)
		}
		tui.Version = Version
		ag, env, pm, proxyMgr, workDir := newSession(cfg, router, *target)

		// Resume prior conversation + prompt history.
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

		prog := tui.New(ag, router, pm.Status(), sessionPath, histInit, resumed, askCh, proxyMgr, cfg, *cfgPath)
		fm, err := prog.Run()
		if err != nil {
			die("tui: %v", err)
		}
		if !tui.WantsSetup(fm) {
			break
		}

		// /setup → re-run the wizard, reload config, relaunch the loop.
		ok, werr := tui.RunWizard(cfg, *cfgPath)
		if werr != nil {
			die("setup: %v", werr)
		}
		if ok {
			if cfg, err = config.Load(*cfgPath); err != nil {
				die("config reload: %v", err)
			}
		}
		*fresh = false // keep the resumed session across a reconfigure
	}
}

// newSession builds the toolchain, env, and agent for a run. Ask is left unset
// for the caller to wire (headless default vs. interactive TUI bridge).
func newSession(cfg *config.Config, router *llm.Router, target string) (*agent.Agent, *tools.Env, *portable.Manager, *proxy.Manager, string) {
	pm := portable.New(cfg.ToolsDir)
	workDir := target
	if workDir == "" {
		workDir = filepath.Join(cfg.WorkDir, "session")
	}
	_ = os.MkdirAll(workDir, 0o755)

	proxyMgr := proxy.New(filepath.Join(workDir, "proxy"))
	env := &tools.Env{WorkDir: workDir, Proxy: proxyMgr}
	// Contextual toolchain: when the agent calls a tool whose binary is missing
	// but auto-installable, fetch it on demand and stream progress.
	env.ResolveBin = func(name string) (string, error) {
		return pm.ResolveOrInstall(name, func(s string) {
			if env.Emit != nil {
				env.Emit("cmd", s)
			}
		})
	}
	reg := tools.NewRegistry()
	ag := agent.New(cfg, router, reg, env)
	return ag, env, pm, proxyMgr, workDir
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

// usage prints the CLI banner + command reference.
func usage() {
	const banner = ` .-.
(o o)  jurig · autonomous reverse-engineering agent
| u |  android · binary · frida
'~-~'`
	fmt.Fprintln(os.Stderr, banner)
	fmt.Fprintf(os.Stderr, "\nversion %s\n", Version)
	fmt.Fprintln(os.Stderr, `
usage:
  jurig                    launch the interactive TUI
  jurig -p "<task>"        headless: run one task, stream to stdout, exit
  jurig -target <dir>      set the session work dir
  jurig -fresh             ignore any saved session

commands:
  setup                    (re)run the first-run provider wizard
  doctor                   print config, providers, and toolchain status
  install <tool>...        fetch a portable tool into tools_dir
  version                  print version
  help                     show this help

flags:`)
	flag.PrintDefaults()
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
