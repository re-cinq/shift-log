## ADDED Requirements

### Requirement: Agent Selection
The `init` command SHALL accept an `--agent` flag to specify which coding agent to configure.

#### Scenario: Init with Claude (default)
- **WHEN** user runs `claudit init` without `--agent`
- **THEN** Claude Code hooks are configured (current behavior)
- **AND** `.claudit/config.json` stores `agent: "claude"`

#### Scenario: Init with Gemini
- **WHEN** user runs `claudit init --agent=gemini`
- **THEN** Gemini CLI hooks are configured in `.gemini/settings.json`
- **AND** `.claudit/config.json` stores `agent: "gemini"`

#### Scenario: Init with OpenCode
- **WHEN** user runs `claudit init --agent=opencode`
- **THEN** a plugin file is generated at `.opencode/plugins/claudit.js`
- **AND** `.claudit/config.json` stores `agent: "opencode"`

#### Scenario: Invalid agent name
- **WHEN** user runs `claudit init --agent=unknown`
- **THEN** an error message lists supported agents: claude, gemini, opencode
- **AND** the command exits with non-zero status

### Requirement: Agent-Specific Hook Configuration
Each agent SHALL have its own hook configuration mechanism.

#### Scenario: Gemini hooks
- **WHEN** configuring Gemini hooks
- **THEN** an `AfterTool` hook is added to `.gemini/settings.json`
- **AND** the hook matches `shell_exec` tool
- **AND** the hook runs `claudit store --agent=gemini`

#### Scenario: OpenCode plugin
- **WHEN** configuring OpenCode hooks
- **THEN** a `.opencode/plugins/claudit.js` file is generated
- **AND** the plugin listens for `tool.execute.after` events
- **AND** the plugin calls `claudit store --agent=opencode`

### Requirement: Agent-Specific Doctor Checks
The `doctor` command SHALL validate configuration for the configured agent.

#### Scenario: Doctor with Gemini
- **WHEN** user runs `claudit doctor` in a Gemini-configured repo
- **THEN** the command checks `.gemini/settings.json` for correct hooks
- **AND** reports OK or FAIL for each hook

#### Scenario: Doctor with OpenCode
- **WHEN** user runs `claudit doctor` in an OpenCode-configured repo
- **THEN** the command checks `.opencode/plugins/claudit.js` exists and is correct
- **AND** reports OK or FAIL

### Requirement: Gemini Session Discovery
The system SHALL discover active Gemini CLI sessions for conversation capture.

#### Scenario: Discover Gemini session
- **WHEN** `claudit store --agent=gemini` is invoked
- **THEN** sessions are discovered at `~/.gemini/tmp/<project_hash>/chats/`

### Requirement: OpenCode Session Discovery
The system SHALL discover active OpenCode CLI sessions from its SQLite database.

#### Scenario: Discover OpenCode session
- **WHEN** `claudit store --agent=opencode` is invoked
- **THEN** sessions are read from the SQLite database at `~/.local/share/opencode/storage`

## MODIFIED Requirements

### Requirement: Session Tracking
The system SHALL track active coding agent sessions to enable conversation capture for manual commits.

#### Scenario: Session start tracking
- **WHEN** a coding agent starts a session in a claudit-enabled repository
- **THEN** the session tracking hook writes session info to `.claudit/active-session.json`
- **AND** the file contains `session_id`, `transcript_path`, `started_at`, `project_path`, and `agent`

#### Scenario: Session end cleanup
- **WHEN** a coding agent ends a session in a claudit-enabled repository
- **THEN** the session tracking hook removes `.claudit/active-session.json`

#### Scenario: Stale session detection
- **WHEN** reading active session state
- **AND** the transcript file has not been modified in 10+ minutes
- **THEN** the session is considered inactive

### Requirement: Claude Code Session Hooks
The `init` command SHALL configure Claude Code hooks for session tracking when `--agent=claude` (or no agent specified).

#### Scenario: SessionStart hook installation
- **WHEN** user runs `claudit init` or `claudit init --agent=claude`
- **THEN** a `SessionStart` hook is added to `.claude/settings.local.json`
- **AND** the hook runs `claudit session-start`

#### Scenario: SessionEnd hook installation
- **WHEN** user runs `claudit init` or `claudit init --agent=claude`
- **THEN** a `SessionEnd` hook is added to `.claude/settings.local.json`
- **AND** the hook runs `claudit session-end`

### Requirement: Doctor Validation
The `doctor` command SHALL validate configuration for the configured agent.

#### Scenario: Check session hooks
- **WHEN** user runs `claudit doctor`
- **THEN** the command checks for the appropriate hooks based on configured agent
- **AND** reports OK or FAIL for each

#### Scenario: Check post-commit hook
- **WHEN** user runs `claudit doctor`
- **THEN** the command checks for `post-commit` hook in `.git/hooks/`
- **AND** reports OK or FAIL based on presence and content
