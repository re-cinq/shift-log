# Tasks: Add Terminal Conversation Viewer

## 1. Implementation

- [x] **1.1** Create terminal renderer for conversations
  - Create `internal/claude/render.go`
  - Format user messages with "User:" prefix
  - Format assistant messages with "Assistant:" prefix
  - Format tool use with tool name and summary
  - Format system messages with "System:" prefix
  - Support color output (respect NO_COLOR env)
  - Verify: Unit tests pass

- [x] **1.2** Implement show command
  - Create `cmd/show.go`
  - Accept optional commit ref argument (default to HEAD)
  - Resolve commit reference
  - Read and decompress conversation from note
  - Render conversation to stdout
  - Handle missing conversation gracefully
  - Verify: Command runs

- [x] **1.3** Add acceptance tests for show command
  - Test: `claudit show <sha>` displays conversation
  - Test: `claudit show` (no args) shows HEAD conversation
  - Test: `claudit show <sha>` with no conversation shows error
  - Test: `claudit show invalid-ref` shows error
  - Test: Output contains User/Assistant prefixes
  - Verify: `ginkgo tests/acceptance` passes

## 2. Version Bump

- [x] **2.1** Bump minor version
  - Update version constant in code
  - Verify: `claudit --version` shows new version
