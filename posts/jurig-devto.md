---
title: "I Built a Fully Autonomous AI Reverse-Engineering Agent in Go"
published: false
description: "Jurig is an autonomous AI agent that decompiles Android apps, greps for secrets, hooks Frida, and captures live traffic through its own MITM proxy — all from a hacker-style terminal UI. Here's how I built it (and the bugs that fought back)."
tags: golang, ai, reverseengineering, cybersecurity
cover_image:
canonical_url:
---

> **Jurig** (Sundanese: *ghost*) — an autonomous AI agent that haunts your binaries.

```
 .-.         ██ ██    ██ ██████  ██  ██████
(o o)        ██ ██    ██ ██   ██ ██ ██
| u |        ██ ██    ██ ██████  ██ ██   ███
|   |   ██   ██ ██    ██ ██   ██ ██ ██    ██
'~-~'    █████   ██████  ██   ██ ██  ██████
        autonomous reverse-engineering agent · android · binary · frida
```

Point it at an APK, XAPK, or native binary and it plans, decompiles, searches,
hooks, captures traffic, and writes you a report — by itself. In one live run it
took a real Android loan app, auto-extracted the XAPK, decompiled **13,367
classes**, grepped the sources, and surfaced a **hardcoded AES key with a
zero IV** plus the full API endpoint map — then asked me whether it should go
dynamic with Frida.

This post is the build story: the architecture, the design bets, and the three
bugs that genuinely fought back.

Repo: **https://github.com/ReverserID/JURIG**

---

## Why build another agent?

Existing "AI reverse engineering" is mostly a pile of MCP servers you wire into a
chat client. That's fine, but I wanted something opinionated:

- **Autonomous**, not chat — it drives a real toolchain end to end.
- **A single portable binary** — no Python venv soup, no MCP daemons.
- **Multi-model** — my Claude subscription, OpenRouter, local Ollama, Kimi, Qwen.
- **A TUI that feels like a hacker tool**, not a log dump.

So: Go. Charmbracelet for the TUI (Bubble Tea + Lipgloss + Glamour). And a hard
rule — **no MCP**. Every capability is a native Go function that shells out to a
portable RE binary, or does the work in pure Go.

---

## Architecture

```
┌─ agent loop ─┐   plan → ask scope → recon → locate → dynamic → report
│              │
│  LLM router  │   anthropic · openai-compat (openrouter/ollama/kimi/qwen) · claude-cli
│              │
│  25+ tools   │   jadx · apktool · radare2 · ghidra · frida · adb · proxy
│              │   + secret_scan · url_extract · manifest · elf/pe_info · search_code
│              │
│  TUI         │   animated ghost header · code cards · model picker · NET panel
└──────────────┘
```

### One wire format, many providers

The whole thing speaks the **Anthropic Messages** protocol internally. A router
adapts it to each backend:

- **`anthropic`** — native Messages API.
- **`openai`** — an adapter that translates Messages ↔ OpenAI Chat Completions
  (tool calls included). This single adapter covers **OpenRouter, Ollama, Kimi
  (Moonshot), and Qwen (Alibaba DashScope)** — they're all OpenAI-compatible.
- **`claude-cli`** — shells out to the `claude` binary for subscription use.

Switching model is live in the TUI (`Ctrl+O`):

```go
func (r *Router) SetSelection(provider, model string) error {
    r.mu.Lock()
    defer r.mu.Unlock()
    if _, ok := r.providers[provider]; !ok {
        return fmt.Errorf("unknown provider %q", provider)
    }
    r.active = config.Selection{Provider: provider, Model: model}
    return nil
}
```

### Tools are just Go

A tool is an interface. The agent loop calls the model with the tool schemas,
gets back `tool_use` blocks, dispatches them, and feeds results back — the
classic loop, but every tool is native:

```go
type Tool interface {
    Name() string
    Description() string
    Schema() map[string]any
    Run(ctx context.Context, input json.RawMessage, env *Env) (string, error)
}
```

The high-signal ones matter most. Instead of letting the model read files one by
one (it *will* burn 60 steps doing that), I gave it recon tools:

- `manifest` — package, permissions, exported components
- `url_extract` — dedup every URL into a host/API map
- `secret_scan` — AWS/GCP/Stripe/JWT/private-key/AES-literal regexes
- `search_code` — regex grep across decompiled sources

The system prompt then enforces **search before read**. Efficiency went from
"hit the step limit" to "done in ~10 steps".

---

## The bugs that fought back

### 1. jadx `NullPointerException` on Java 21

First real APK, and jadx 1.5.1 face-planted:

```
java.lang.NullPointerException
    at jadx.plugins.tools.JadxPluginsTools.getEnabledPluginJars(...)
```

`user.home` was set. The plugin loader in 1.5.x just breaks on Java 21 / Windows.
Fix: **pin jadx 1.4.7** (pre plugin-loader). Decompiled 13k classes on the first
try. Sometimes the fix is a version number.

### 2. Kimi rejects `"required": null`

Switched a run to Kimi and every request 400'd:

```
tools.function.parameters is not a valid moonshot flavored json schema,
details: <At path 'required': required must be an array>
```

My schema helper used a Go variadic. Called with no required fields it's `nil`,
which marshals to JSON `null`. Anthropic and OpenAI tolerate it; Kimi/Moonshot
don't. One line, plus a regression test so it never comes back:

```go
func schema(props map[string]any, required ...string) map[string]any {
    if required == nil {
        required = []string{} // never marshal to null — Kimi rejects it
    }
    return map[string]any{"type": "object", "properties": props, "required": required}
}
```

### 3. The TUI box kept shattering

`adb` output would tear the bordered viewport apart — stray border chars
scattered mid-screen. I first blamed word-wrapping and hard-wrapped everything.
Still broke.

The real culprit: **Windows `adb` emits `CRLF`**. After splitting on `\n`, the
leftover `\r` yanks the cursor back to column 0 and the next characters overprint
the border. Sanitizing tool output fixed it instantly:

```go
func sanitize(s string) string {
    s = strings.ReplaceAll(s, "\r\n", "\n")
    s = strings.ReplaceAll(s, "\r", "")
    return strings.Map(func(r rune) rune {
        if r == '\n' || r == '\t' { return r }
        if r < 0x20 || r == 0x7f { return -1 } // drop control bytes
        return r
    }, s)
}
```

Lesson: when a TUI "randomly" corrupts, suspect control characters before layout.

---

## Making it feel alive

A few touches that turned a functional tool into one people actually want to run:

- **Animated ghost header** — the mascot blinks, glances around, and a neon
  color shimmer travels across the wordmark. It's pinned; the transcript scrolls
  beneath it.
- **Code cards** — when the agent reads a source file, the TUI shows a small
  syntax-highlighted (Chroma) preview. The *model* still gets the full file; your
  eyes get 12 tidy lines.
- **Ask menu** — when the agent needs scope ("secrets? auth flow? may I run
  Frida on a device?"), a multi-select checkbox menu pops up (↑/↓, Space, Enter).
- **`Esc` to interrupt** — stop the agent mid-run and steer it, Claude-Code style.
- **Live NET panel** — start the built-in MITM proxy and captured
  request/response pairs stream into a side panel. The layout is responsive: wide
  terminal splits, narrow terminal hides the panel.

### The native proxy (no Burp)

Dynamic network capture is a first-class tool, built on `elazarl/goproxy` — a
real in-process MITM proxy with an exported CA:

```
proxy start → adb set http_proxy + install CA → frida_preset ssl_unpin
            → drive the app → proxy flows
```

The agent orchestrates all of that; you just watch the requests land.

---

## Dynamic instrumentation, presets included

The model shouldn't have to hand-write Frida JS every time, so `frida_preset`
ships battle-tested scripts:

- `ssl_unpin` — universal Android pinning bypass (TrustManager + OkHttp +
  Conscrypt)
- `hook` — hook every overload of `Class!method`, dump args + return
- `dump_class`, `list_classes`, `trace_http`

Combined with the proxy, that's a full static → dynamic → network pipeline in one
binary.

---

## Try it

Grab a build from the [releases page](https://github.com/ReverserID/JURIG/releases):

```bash
jurig doctor      # check providers + toolchain
jurig             # first run launches an interactive setup wizard
```

First run walks you through provider + model, drops you into the TUI, and
auto-installs missing tools (jadx, apktool) on demand.

> ⚠️ For authorized security research and education only. Analyze apps you own or
> have explicit permission to test.

---

## What's next

- ACP backend so the Claude subscription path drives tools natively
- Ghidra decompile scripting for deeper native analysis
- Linux/macOS builds + CI

If you build agents, write Go, or do mobile RE — I'd love feedback. Star the repo
and tell me what tool you'd add next.

**GitHub:** https://github.com/ReverserID/JURIG

Built with Go, Bubble Tea, Lipgloss, Glamour, Chroma, and goproxy.
