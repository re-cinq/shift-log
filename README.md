# Shift Log

[![License](https://img.shields.io/badge/License-AINAL-blue.svg)](https://github.com/re-cinq/ai-native-application-license)
[![Go](https://img.shields.io/badge/Go-1.21+-00ADD8.svg)](https://go.dev)
[![Git Notes](https://img.shields.io/badge/Git%20Notes-Supported-green.svg)](https://git-scm.com/docs/git-notes)

Save, view, and search coding agent conversation history, persisted as Git Notes.

## ✨ Features

- 📝 **Automatic Capture** — Conversations saved automatically on commits
- 🔍 **Full-text Search** — Search through past agent sessions
- 📊 **Summarise** — AI-powered conversation summaries
- 🔄 **Resume Sessions** — Pick up where you left off
- 🌐 **Web Viewer** — Visualize conversations in browser
- 🤝 **Team Sync** — Share conversations across your team via Git

## 🚀 Quick Start

### Installation

```bash
curl -fsSL https://raw.githubusercontent.com/re-cinq/shift-log/master/scripts/install.sh | bash
```

### Initialize in a Git Repo

```bash
cd your-project
shiftlog init --agent=<agent>
```

Where `<agent>` is `claude` (default), `codex`, `copilot`, `gemini`, or `opencode`.

Now work with your coding agent as normal. Whenever you or the agent commits, the conversation since the last commit will be attached as a Git Note.

## 📋 Supported Agents

| Agent | Init Command | Hook Method |
|-------|--------------|-------------|
| **Claude Code** | `shiftlog init` (default) | `.claude/settings.json` |
| **Codex CLI** | `shiftlog init --agent=codex` | Post-commit hook |
| **Copilot CLI** | `shiftlog init --agent=copilot` | `.github/hooks/shiftlog.json` |
| **Gemini CLI** | `shiftlog init --agent=gemini` | `.gemini/settings.json` |
| **OpenCode** | `shiftlog init --agent=opencode` | `.opencode/plugins/shiftlog.js` |

## 💡 Usage

**See your conversation history:**

```bash
shiftlog list
```

**Search through past conversations:**

```bash
shiftlog search "authentication"              # Text search
shiftlog search --agent claude --branch main # Filter by metadata
shiftlog search "jwt" --regex --context 2    # Regex with context
```

**Summarise a conversation:**

```bash
shiftlog summarise         # Summarise HEAD conversation
shiftlog tldr HEAD~1       # Alias, same result
shiftlog tldr --focus="security"
```

**Resume a past session:**

```bash
shiftlog resume abc123    # By commit SHA
shiftlog resume HEAD~3    # By git ref
```

**View in browser:**

```bash
shiftlog serve
```

**Sync with remote:**

```bash
shiftlog sync pull
shiftlog sync push
```

## ⚙️ Commands Reference

| Command | Description |
|---------|-------------|
| `shiftlog init` | Initialize shiftlog in the current repo |
| `shiftlog list` | List commits with stored conversations |
| `shiftlog search [query]` | Search through stored conversations |
| `shiftlog show [ref]` | Show conversation history for a commit |
| `shiftlog summarise [ref]` | Summarise a conversation |
| `shiftlog resume <commit>` | Resume a coding agent session |
| `shiftlog serve` | Start the web visualization server |
| `shiftlog doctor` | Diagnose configuration issues |
| `shiftlog sync push/pull` | Sync notes with remote |
| `shiftlog remap` | Remap orphaned notes after rebase |

## 🔧 Requirements

- Git
- One of: Claude Code, Codex CLI, Copilot CLI, Gemini CLI, or OpenCode

## 🤖 How It Works

Shiftlog uses [Git Notes](https://git-scm.com/docs/git-notes) to attach conversations to commits under `refs/notes/shiftlog`. This keeps `git log` clean while persisting valuable context.

```
┌─────────────┐     commit     ┌─────────────┐
│ Claude Code │ ──────────────▶│   Git       │
└─────────────┘                └─────────────┘
      │                              │
      │  post-commit hook             │  git notes
      ▼                              ▼
┌─────────────┐                ┌─────────────┐
│ shiftlog    │ ──────────────▶│ Git Note    │
│ capture     │                │ refs/notes/ │
└─────────────┘                │ shiftlog    │
                               └─────────────┘
```

## 👥 Team Collaboration

When multiple developers use shiftlog on the same repository, conversation notes sync automatically via git push/pull hooks. Notes are merged seamlessly because each developer typically annotates different commits.

If notes diverge:

```bash
shiftlog sync pull   # Fetch and merge remote notes
shiftlog sync push   # Now succeeds
```

## 📌 Git Operations

### Worktrees
Shiftlog is worktree-safe. Each worktree sees only conversations for commits on its own branch.

### Rebase
Notes automatically follow commits during rebase via `notes.rewriteRef`. No manual steps needed.

### GitHub Rebase Merge
For PRs merged via "Rebase and merge", run `shiftlog remap` after pulling to remap orphaned notes to new commit SHAs.

## 📊 Comparison

| Feature | [Entire](https://entire.io) | Shift Log |
|---------|---------------------------|-----------|
| **Agents** | Claude Code, Gemini CLI | Claude, Codex, Copilot, Gemini, OpenCode |
| **Storage** | Custom checkpoints | Git Notes (standard) |
| **Resume Sessions** | ❌ | ✅ |
| **Web Viewer** | ❌ | ✅ |
| **Rebase Support** | ❌ | ✅ |
| **Open Source** | ✅ | ✅ |

## ❓ Why?

To understand _how_ and _why_ a commit was made, you need to see the conversation that led to it. ShiftLog makes that possible.

## 📄 License

[AI Native Application License (AINAL)](https://github.com/re-cinq/ai-native-application-license) — see [LICENSE](LICENSE).

---

*README optimized with [Gingiris README Generator](https://gingiris.github.io/github-readme-generator/)*
