## 1. MCP Server Core
- [ ] 1.1 Add `github.com/modelcontextprotocol/go-sdk` to `go.mod`
- [ ] 1.2 Create `internal/mcp/server.go` with MCP server setup and tool registration
- [ ] 1.3 Implement `claudit_list_conversations` tool handler
- [ ] 1.4 Implement `claudit_get_conversation` tool handler
- [ ] 1.5 Implement `claudit_list_branches` tool handler
- [ ] 1.6 Extract shared data retrieval from `internal/web/handlers.go` if needed

## 2. CLI Command
- [ ] 2.1 Create `cmd/mcp_server.go` with `claudit mcp-server` Cobra subcommand
- [ ] 2.2 Wire stdio transport to MCP server

## 3. Configuration Generation
- [ ] 3.1 Create `internal/mcp/config.go` for `.vscode/mcp.json` generation
- [ ] 3.2 Add `--mcp` flag to `claudit init` to generate VS Code MCP config
- [ ] 3.3 Handle merging with existing `.vscode/mcp.json`

## 4. Tests
- [ ] 4.1 Unit tests for tool handlers with mock git data (`internal/mcp/server_test.go`)
- [ ] 4.2 Integration test: pipe `tools/list` JSON-RPC request, verify 3 tools returned
- [ ] 4.3 Integration test: pipe `tools/call` for each tool, verify responses
- [ ] 4.4 Acceptance test for `claudit init --mcp` generating `.vscode/mcp.json`

## 5. Validation
- [ ] 5.1 `go vet ./...` passes
- [ ] 5.2 `go test ./internal/mcp/...` passes
- [ ] 5.3 `go test ./tests/acceptance/...` passes
- [ ] 5.4 Manual verification: `claudit mcp-server` speaks MCP on stdio
