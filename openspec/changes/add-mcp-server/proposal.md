# Change: Add MCP Server for Conversation Data Access

## Why
VS Code Copilot (and other MCP-aware AI tools) can query external data sources via the Model Context Protocol. An MCP server exposes claudit's stored conversations as tools, letting Copilot answer questions like "what did we discuss when implementing feature X?" without leaving the editor.

## What Changes
- Add `claudit mcp-server` subcommand that runs a stdio MCP server
- Expose three tools: `claudit_list_conversations`, `claudit_get_conversation`, `claudit_list_branches`
- Add `internal/mcp/server.go` for server setup and tool handlers (reusing web handler data retrieval)
- Add `internal/mcp/config.go` for `.vscode/mcp.json` generation
- Extend `claudit init` with `--mcp` flag to generate VS Code MCP configuration
- Add `github.com/modelcontextprotocol/go-sdk` dependency

## Impact
- Affected specs: cli-foundation (new command and init flag)
- Affected code: `cmd/mcp_server.go`, `internal/mcp/`, `cmd/init.go`, `go.mod`
- New dependency: `github.com/modelcontextprotocol/go-sdk`
