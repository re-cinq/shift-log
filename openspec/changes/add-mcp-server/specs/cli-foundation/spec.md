## ADDED Requirements
### Requirement: MCP Server Command
The CLI SHALL provide a `claudit mcp-server` command that runs a Model Context Protocol server over stdio.

#### Scenario: Start MCP server
- **WHEN** user runs `claudit mcp-server` from within a git repository
- **THEN** an MCP server starts on stdio using JSON-RPC transport
- **AND** the server registers three tools: `claudit_list_conversations`, `claudit_get_conversation`, `claudit_list_branches`

#### Scenario: List tools
- **WHEN** an MCP client sends a `tools/list` request
- **THEN** the server responds with schemas for all three tools

#### Scenario: List conversations tool
- **WHEN** an MCP client calls `claudit_list_conversations` with optional `limit`, `offset`, and `branch` parameters
- **THEN** the server returns an array of commits with conversation metadata (SHA, message, author, date, message count)

#### Scenario: Get conversation tool
- **WHEN** an MCP client calls `claudit_get_conversation` with a `commit_sha` parameter
- **THEN** the server returns the full conversation for that commit (SHA, session ID, timestamp, message count, transcript)

#### Scenario: Get conversation incremental mode
- **WHEN** an MCP client calls `claudit_get_conversation` with `incremental: true`
- **AND** a parent commit has a stored conversation
- **THEN** only entries since the parent commit are returned

#### Scenario: List branches tool
- **WHEN** an MCP client calls `claudit_list_branches`
- **THEN** the server returns all branches with conversation counts

#### Scenario: Run outside git repository
- **WHEN** user runs `claudit mcp-server` outside a git repository
- **THEN** the command exits with an error indicating it must be run from within a git repository

### Requirement: MCP Configuration Generation
The `init` command SHALL support generating VS Code MCP configuration with a `--mcp` flag.

#### Scenario: Generate MCP config
- **WHEN** user runs `claudit init --mcp`
- **THEN** a `.vscode/mcp.json` file is created with a `claudit` server entry
- **AND** the entry specifies `type: "stdio"`, the absolute path to the claudit binary, and `args: ["mcp-server"]`

#### Scenario: Merge with existing MCP config
- **WHEN** `.vscode/mcp.json` already exists with other server entries
- **THEN** the claudit entry is added or updated without removing existing entries

#### Scenario: MCP config without init
- **WHEN** user runs `claudit init --mcp` in a repository not yet initialized with claudit
- **THEN** the full init process runs (hooks, gitignore, etc.) in addition to generating MCP config
