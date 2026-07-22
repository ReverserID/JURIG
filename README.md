# Jurig

```
 .-.         ██ ██    ██ ██████  ██  ██████
(o o)        ██ ██    ██ ██   ██ ██ ██
| u |        ██ ██    ██ ██████  ██ ██   ███
|   |   ██   ██ ██    ██ ██   ██ ██ ██    ██
'~-~'    █████   ██████  ██   ██ ██  ██████
        autonomous reverse-engineering agent · android · binary · frida
```

> **Jurig** (Sundanese: *ghost*) — an autonomous AI agent that haunts your
> binaries. In the TUI the ghost blinks, bobs, and shimmers while it works.

Fully autonomous, AI-agentic **reverse-engineering framework** with a TUI.
Android-first (APK/DEX) plus native binaries. Written in Go, no MCP — every
tool is a native subprocess wrapper around a portable RE binary.

```
┌─ JURIG ────────────────────────────────────────────┐
│ agent loop  →  tool registry  →  portable toolchain │
│      ↑              │                                │
│  LLM router   jadx · radare2 · apktool · adb · frida │
│  (anthropic /  · strings · shell · read/write_file   │
│   openrouter /                                       │
│   claude-cli)                                        │
└─────────────────────────────────────────────────────┘
```

## Design

| Concern | Choice |
|---|---|
| Language / TUI | Go + Bubble Tea + Lipgloss + **Glamour** (markdown reports) |
| Agent | Own plan→act→observe loop (`internal/agent`) — no framework lock-in |
| Tools | Native Go subprocess wrappers (`internal/tools`) — **no MCP** |
| LLM | One wire format (Anthropic **Messages** protocol) behind a router |
| Providers | `anthropic` (direct), `openrouter` (Anthropic "skin"), `claude-cli` (subscription) |
| Portable tools | `internal/portable` resolves bundled binaries → PATH; installs from a catalog |

### LLM providers — multi-model, switchable live

Two wire protocols behind one router:

- **`anthropic` kind** — native Messages API (`/v1/messages`).
- **`openai` kind** — OpenAI Chat Completions (`/chat/completions`) with an
  internal Messages↔OpenAI translator (tool-calls included). Covers **any**
  OpenAI-compatible endpoint.

Presets shipped (`config.json` → `providers`):

| Provider | Kind | Endpoint | Key env |
|---|---|---|---|
| **mimo** (default) | openai | token-plan-sgp.xiaomimimo.com/v1 | `MIMO_API_KEY` |
| anthropic | anthropic | api.anthropic.com | `ANTHROPIC_API_KEY` |
| **openrouter** | openai | openrouter.ai/api/v1 | `OPENROUTER_API_KEY` |
| **ollama** (local) | openai | localhost:11434/v1 | — (local) |
| **kimi** | openai | api.kimi.com/coding/v1 | `KIMI_API_KEY` |
| **moonshot** | openai | api.moonshot.ai/v1 | `MOONSHOT_API_KEY` |
| **dashscope / Qwen** (Alibaba) | openai | dashscope-intl…/compatible-mode/v1 | `DASHSCOPE_API_KEY` |
| cursor (subscription) | openai | cursor-openai-api bridge | — (browser auth) |
| claude-cli (subscription) | claude-cli | `claude` binary | — |

Pick provider+model **live in the TUI with Ctrl+O**, or set
`JURIG_PROVIDER` + `JURIG_MODEL`, or edit `active` in `config.json`. Add models
by extending a provider's `models` list. Point any provider at a different base
URL (e.g. a self-hosted gateway) via `base_url`.

**Ollama**: `ollama serve` + `ollama pull qwen2.5-coder:14b`, then Ctrl+O →
pick `ollama/…`. No key needed.

**claude-cli** (Claude Code subscription): Anthropic blocks subscription auth in
third-party HTTP harnesses (Apr 2026), so this path shells out to the `claude`
binary. Advisory/chat only — no native tool-calling; use an `openai`/`anthropic`
provider for the full autonomous tool loop.

**cursor** (Cursor subscription): API tokens are expensive; a Cursor Pro/Max
subscription is cheaper. Jurig ships **native Cursor auth**:

```sh
jurig cursor login     # PKCE browser login → ~/.jurig/cursor-auth.json
jurig cursor status    # verify / auto-refresh
jurig cursor token     # print a valid access token
```

Cursor chat is a stateful Agent protocol (Connect-RPC + protobuf over HTTP/2),
so today the `cursor` provider talks OpenAI-compat to the
[cursor-openai-api](https://github.com/shawtyygabriel/cursor-openai-api) bridge,
which Jurig can launch for you (needs `bun` or `node`/`npx`):

```sh
jurig cursor bridge login    # one-time OAuth (opens browser)
jurig cursor serve           # runs the bridge on :3000 — keep this terminal open
# in another terminal:
jurig                        # Ctrl+O → cursor/<model>
```

`jurig cursor serve [port]` shells out to `cursor-openai-api` via bunx/npx and
the `cursor` provider defaults to `http://127.0.0.1:3000/v1`. Override with
`CURSOR_BASE_URL` for a custom port.

Jurig also ships **native Cursor auth** (`jurig cursor login/status/token`) for a
future no-bridge Go Agent client (in progress). **Using Cursor outside the editor
may violate its ToS — your account, your risk.**

## Build

```sh
go build -o jurig ./cmd/jurig
```

## Use

No flags needed. First run (or when the active provider has no key) launches an
interactive **setup wizard**: pick provider (OpenRouter / Kimi / Qwen / Ollama /
Anthropic / Claude subscription) → key → model → optional tool pre-install →
saved to `~/.jurig/config.json`.

```sh
./jurig                              # first run → wizard, then TUI
./jurig setup                        # re-run the wizard anytime
./jurig doctor                       # show provider + toolchain status
./jurig install jadx                 # pre-warm a tool (also auto-installs on demand)
./jurig -p "analyze ./app.apk"       # headless one-shot (skips wizard; needs a key set)
./jurig -target ./work/app -p "..."  # pin a per-target work dir
```

### Keybindings (TUI)

| Key | Action |
|-----|--------|
| Enter | run instruction / submit |
| ↑ / ↓ | prompt history |
| PgUp / PgDn | scroll transcript |
| Ctrl+F | search transcript (Enter = next match) |
| Ctrl+O | switch provider / model |
| F1 | help overlay |
| Ctrl+C | abort run / quit |

When the agent asks a scoping question, an **answer menu** appears: ↑/↓ move,
**Space or 1-9 toggle (multi-select)**, Enter confirm.

**Session resume:** the conversation + prompt history persist to
`<work_dir>/session.json` and auto-restore on next launch (`--fresh` starts clean).

**Code cards:** whenever the agent reads a source file, the TUI shows a small
syntax-highlighted preview — the model still receives the full file.

## Tools (25, all native / subprocess, no MCP)

**Recon & triage**
| Tool | Purpose |
|------|---------|
| `manifest` | parse AndroidManifest → package, permissions, exported components |
| `url_extract` | pull + dedupe all URLs → API/host map |
| `secret_scan` | categorized hardcoded-secret scan (AWS/GCP/JWT/keys/AES…) |
| `native_libs` | list `lib/<abi>/*.so` + ABIs (JNI surface) |
| `search_code` | regex grep over sources — locate before reading |
| `ask_user` | agent asks the operator (multi-select menu) |

**Static**
| Tool | Purpose |
|------|---------|
| `jadx` | APK/DEX/XAPK → Java (auto-extracts bundles, rich summary) |
| `apktool` | APK → smali + decoded manifest |
| `radare2` / `ghidra` | native binary disasm / headless decompile |
| `elf_info` / `pe_info` | parse ELF/PE headers, symbols, imports (native) |
| `hexdump` / `strings` | raw bytes / printable strings |
| `unzip` | native zip/apk/xapk/apks extraction (no shell) |

**Dynamic & network**
| Tool | Purpose |
|------|---------|
| `adb` | device control |
| `frida_ps` | list device apps/processes |
| `frida` | run a custom Frida JS script |
| `frida_preset` | built-in: `ssl_unpin`, `list_classes`, `dump_class`, `hook`, `trace_http` |
| `proxy` | native Go MITM proxy (goproxy) — capture live HTTPS traffic |
| `http_request` | replay/test discovered API endpoints |
| `download` | fetch a URL to the work dir |

**Live network capture:** `proxy start` spins up an in-process MITM proxy and
exports a CA. The agent points the device at it (adb) + installs the CA + runs
`frida_preset ssl_unpin`, then reads flows with `proxy flows`. Captured
requests/responses stream live in a **NET side-panel** in the TUI (shown when
the terminal is wide enough — the layout is fully responsive).

**Filesystem / shell:** `read_file`, `write_file`, `list_dir`, `shell` (PowerShell default on Windows).

Header shows live **token usage** (`Nk↑ Nk↓`) as the agent runs.

In the TUI: type a target/instruction, **Enter** to run, **PgUp/PgDn** to
scroll, **Ctrl+C** to abort/quit. The agent decompiles, inspects, hooks with
frida, and writes a Markdown report rendered inline with Glamour.

## Toolchain — installs itself on demand

Tools resolve from PATH or `tools_dir` (`~/.jurig/tools`). When the agent
calls a tool whose binary is **missing but catalog-known** (jadx, apktool), it
**auto-downloads it mid-run** — no manual step. `jurig install <tool>` still
works for pre-warming. Heavy/licensed tools (ghidra) stay manual.

Detected/installable:

| Tool | Purpose | Get it |
|---|---|---|
| jadx | APK/DEX → Java (pinned 1.4.7; 1.5.x NPEs on Java 21/Win) | `jurig install jadx` |
| apktool | APK → smali + manifest | `jurig install apktool` (jar; needs wrapper) |
| radare2 | native disasm/analysis | system package |
| adb | device control | Android platform-tools |
| frida | dynamic instrumentation | `pip install frida-tools` + frida-server on device |
| ghidra | headless decompile | install manually, point `tools_dir/ghidra` at it |

## Config

`~/.jurig/config.json` (see `config.example.json`). Env overrides:
`ANTHROPIC_API_KEY`, `OPENROUTER_API_KEY`, `JURIG_PROVIDER`,
`ANTHROPIC_BASE_URL`, `JURIG_TOOLS_DIR`.

## Layout

```
cmd/jurig          entrypoint, TUI/headless/install/doctor
internal/config    config + env overlay
internal/llm       Messages types, router, anthropic + openai-compat + claude-cli
internal/tools     registry + tools (shell,fs,unzip,strings,radare2,jadx,apktool,adb,frida)
internal/portable  resolve + install portable binaries
internal/agent     autonomous loop + system prompt + events
internal/tui       Bubble Tea app, Glamour rendering
```

## Status / roadmap

MVP works: agent loop, provider router, tool registry, TUI, portable manager.
Next: response streaming, network capture (mitmproxy) tool, ghidra headless
tool, per-target session persistence, frida-server auto-push.
