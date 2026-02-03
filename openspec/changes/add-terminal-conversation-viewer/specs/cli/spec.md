## ADDED Requirements

### Requirement: Terminal Conversation Viewer
The CLI SHALL provide a `show` command that displays conversation history for a given commit reference in the terminal.

#### Scenario: Show conversation for commit
- **WHEN** user runs `claudit show <ref>` where `<ref>` is a valid commit with a stored conversation
- **THEN** the conversation is displayed in the terminal with formatted output

#### Scenario: Show conversation for HEAD
- **WHEN** user runs `claudit show` without arguments
- **THEN** the conversation for HEAD is displayed (if one exists)

#### Scenario: Commit has no conversation
- **WHEN** user runs `claudit show <ref>` where `<ref>` has no stored conversation
- **THEN** an error message is displayed indicating no conversation exists for that commit

#### Scenario: Invalid commit reference
- **WHEN** user runs `claudit show <ref>` where `<ref>` is not a valid commit
- **THEN** an error message is displayed indicating the commit could not be found

### Requirement: Conversation Output Format
The terminal output SHALL be formatted for readability with clear visual separation between messages.

#### Scenario: User messages displayed
- **WHEN** conversation contains user messages
- **THEN** they are displayed with a "User:" prefix and distinct formatting

#### Scenario: Assistant messages displayed
- **WHEN** conversation contains assistant messages
- **THEN** they are displayed with an "Assistant:" prefix and distinct formatting

#### Scenario: Tool use displayed
- **WHEN** conversation contains tool use entries
- **THEN** tool name and summary are displayed (full details optional/collapsible in future interactive mode)

#### Scenario: System messages displayed
- **WHEN** conversation contains system messages
- **THEN** they are displayed with a "System:" prefix (can be filtered with flags in future)
