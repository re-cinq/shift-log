## Context
The Model Context Protocol (MCP) is a standard for AI tools to access external data. VS Code Copilot, Claude Code, and other agents support MCP servers. Claudit already has rich data retrieval logic in `internal/web/handlers.go` — the MCP server wraps this in JSON-RPC over stdio.

## Goals / Non-Goals
- Goals: Let MCP-aware AI tools query claudit conversation history; reuse existing data retrieval; simple setup via `claudit init --mcp`
- Non-Goals: Writable MCP tools (no creating/modifying conversations); HTTP transport; custom MCP resources or prompts

## Decisions
- **stdio transport**: MCP standard for local tools. VS Code spawns the process and communicates via stdin/stdout JSON-RPC.
- **Official Go SDK**: Use `github.com/modelcontextprotocol/go-sdk` (maintained with Google, supports 2025-11-25 MCP spec).
- **Reuse web handler logic**: The three tools map directly to existing data retrieval in `internal/web/handlers.go` (`getCommitList`, `buildNoteSet`, `getStoredOrWriteError`).
- **Three tools only**: Minimal surface area — list conversations, get conversation, list branches. More tools can be added later.
- **`--mcp` flag on init**: Generates `.vscode/mcp.json` with absolute path to claudit binary. Can be committed to source control.

## MCP Tools

### `claudit_list_conversations`
List git commits that have AI conversation history.

**Input schema:**
```json
{
  "type": "object",
  "properties": {
    "limit": {"type": "number", "default": 20},
    "offset": {"type": "number", "default": 0},
    "branch": {"type": "string", "description": "Optional branch name filter"}
  }
}
```

**Returns:** Array of `{sha, message, author, date, messageCount}` objects.

**Reuses:** `getCommitList()`, `buildNoteSet()` from `internal/web/handlers.go`

### `claudit_get_conversation`
Retrieve the full AI conversation for a specific commit.

**Input schema:**
```json
{
  "type": "object",
  "properties": {
    "commit_sha": {"type": "string"},
    "incremental": {"type": "boolean", "default": false}
  },
  "required": ["commit_sha"]
}
```

**Returns:** `{sha, sessionId, timestamp, messageCount, transcript, isIncremental}` object.

**Reuses:** `storage.GetStoredConversation()`, `stored.ParseTranscript()`

### `claudit_list_branches`
List git branches with conversation counts.

**Input schema:**
```json
{
  "type": "object",
  "properties": {}
}
```

**Returns:** Array of `{name, headSha, isCurrent, conversationCount}` objects.

**Reuses:** `git.ListBranches()`, `buildAllNoteSet()`

## Data Flow
```
VS Code Copilot Chat
  -> MCP JSON-RPC (stdio)
claudit mcp-server
  -> internal/mcp/server.go (tool dispatch)
  -> internal/web/handlers.go (data retrieval)
  -> internal/storage/ (conversation decompression)
  -> git notes (refs/notes/claude-conversations)
```

## Generated Configuration

`.vscode/mcp.json`:
```json
{
  "servers": {
    "claudit": {
      "type": "stdio",
      "command": "/absolute/path/to/claudit",
      "args": ["mcp-server"]
    }
  }
}
```

## Risks / Trade-offs
- New dependency (`go-sdk`) increases binary size slightly — acceptable for MCP support
- Web handler logic may need minor refactoring to decouple from HTTP response writing — extract shared data functions
- MCP server gets `repoDir` from `git.GetRepoRoot()` at startup — must be run from within a git repo

## Open Questions
- Should `claudit_get_conversation` return parsed messages or raw transcript?
- Should the MCP server support MCP resources (e.g., `claudit://conversation/{sha}`) in addition to tools?
