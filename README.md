# Claudit

Store, resume, and visualize Claude Code conversations as Git Notes.

## What is Claudit?

Claudit is a CLI tool that captures your Claude Code conversation history and stores it directly in your Git repository using Git Notes. This allows teams to:

- **Preserve AI context** - Keep the reasoning behind code changes alongside the commits
- **Share knowledge** - Team members can see the AI-assisted development process
- **Resume sessions** - Pick up where you left off on any commit
- **Visualize history** - Browse conversations in a web interface

## Status: All Milestones Complete

Claudit is feature-complete with full conversation storage, session resume, and web visualization.

## Commands

### `claudit init` - Set up repository
```bash
cd your-project
claudit init
```

Configures:
- Claude Code's PostToolUse hook to capture conversations on commit
- Git hooks (pre-push, post-merge, post-checkout) for automatic note syncing

### `claudit store` - Capture conversation (automatic)

Called automatically by Claude Code hook when you commit. Compresses and stores the conversation as a Git Note.

### `claudit list` - Show commits with conversations
```bash
claudit list
```

Lists all commits that have stored conversations, showing:
- Commit SHA
- Date
- Message preview
- Number of messages

### `claudit resume <commit>` - Resume a session
```bash
claudit resume abc123       # Resume from short SHA
claudit resume HEAD~2       # Resume from relative ref
claudit resume feature-branch  # Resume from branch name
claudit resume abc123 --force  # Skip uncommitted changes warning
```

Restores the conversation and launches Claude Code to continue the session.

### `claudit serve` - Web visualization
```bash
claudit serve              # Start on port 8080, open browser
claudit serve --port 3000  # Custom port
claudit serve --no-browser # Don't open browser automatically
```

Opens a web interface showing:
- Commit list with conversation indicators
- Full conversation viewer with collapsible tool uses
- Resume button to continue any session

### `claudit sync` - Manual sync
```bash
claudit sync push              # Push notes to origin
claudit sync pull              # Fetch notes from origin
claudit sync push --remote upstream  # Push to different remote
```

## How It Works

1. **Hook Integration** - When Claude Code runs a git commit, the PostToolUse hook triggers `claudit store`
2. **Compression** - The conversation transcript is gzip compressed and base64 encoded
3. **Storage** - Stored as a Git Note in `refs/notes/claude-conversations` namespace
4. **Sync** - Git hooks automatically sync notes when you push/pull
5. **Resume** - Restores session files to Claude's expected location and launches with `--resume`

### Storage Format

Each note contains:
```json
{
  "version": 1,
  "session_id": "uuid",
  "timestamp": "2024-01-15T10:30:00Z",
  "project_path": "/path/to/repo",
  "git_branch": "feature-branch",
  "message_count": 42,
  "checksum": "sha256:abc123...",
  "transcript": "<compressed conversation>"
}
```

## Installation

```bash
# Build from source
make build

# The binary is created at ./claudit
```

### Requirements

- Git CLI
- Go 1.21+ (for building)
- Claude CLI (for resume functionality)

## Quick Start

```bash
# 1. Initialize in your project
cd your-project
claudit init

# 2. Start a Claude Code session and make commits
# Conversations are captured automatically

# 3. Push to share (notes sync automatically with git push)
git push

# 4. View your conversation history
claudit list

# 5. Browse in web UI
claudit serve

# 6. Resume any session
claudit resume <commit>
```

## Development

```bash
make build       # Build binary
make test        # Run unit tests
make acceptance  # Run acceptance tests
make all         # Run all tests
```

## Test Suite

42 acceptance tests covering:
- CLI foundation (help, version)
- Init command (hooks setup)
- Store command (note creation, compression)
- Sync command (push/pull, git hooks)
- Resume command (session restore, checkout)
- List command (conversation listing)
- Serve command (web server basics)

## Milestones

- **Milestone 1** ✅ - Store conversations as Git Notes
- **Milestone 2** ✅ - Resume sessions from any commit
- **Milestone 3** ✅ - Web visualization of commit graph with conversations

## License

See LICENSE file for details.
