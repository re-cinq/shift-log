# claudit

Automatically save Claude Code conversations as Git Notes on every commit. Claude. Audit. See what we did there? 🥁

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/re-cinq/claudit/master/scripts/install.sh | bash
claudit init
```

Now work with `claude` as you would normally. Whenever you or Claude Code commit, the conversation since the last commit will be attached to that commit as a Git Note.

## Usage

**See what conversations you have:**

```bash
claudit list
```

**Resume a past session:**

```bash
claudit resume abc123    # By commit SHA
claudit resume HEAD~3    # By git ref
```

**View in your browser:**

```bash
claudit serve
```

## Why?

In order to understand _how_ and _why_ a commit was made, we need to see the conversation that led to it.

## How It Works

Claudit uses [Git Notes](https://git-scm.com/docs/git-notes) to attach conversations to commits, stored under `refs/notes/claude-conversations` to keep `git log` clean. When you run `claudit init`, it sets up hooks so:

1. When Claude makes a commit, the conversation is saved automatically
2. When you make a commit during a Claude session, it's saved too
3. When you push/pull, conversations sync with the remote

No extra steps needed during your normal workflow.

To view notes directly with git: `git log --notes=claude-conversations`

## Commands

| Command                   | Description                                |
| ------------------------- | ------------------------------------------ |
| `claudit init`            | Initialize claudit in the current repo     |
| `claudit list`            | List commits with stored conversations     |
| `claudit show [ref]`      | Show conversation history for a commit     |
| `claudit resume <commit>` | Resume a Claude session from a commit      |
| `claudit serve`           | Start the web visualization server         |
| `claudit doctor`          | Diagnose claudit configuration issues      |
| `claudit debug`           | Toggle debug logging                       |
| `claudit sync push/pull`  | Sync conversation notes with remote        |

## Requirements

- Git
- Claude Code CLI (for resume)

## License

[AI Native Application License (AINAL)](https://github.com/re-cinq/ai-native-application-license) — see [LICENSE](LICENSE).
