## MODIFIED Requirements

### Requirement: Incremental API Parameter
The conversation detail API SHALL support an `incremental` query parameter.

#### Scenario: Get incremental conversation
- **WHEN** client requests `GET /api/commits/<sha>?incremental=true`
- **AND** a parent commit has a stored conversation
- **THEN** response includes only entries since parent's last entry
- **AND** response includes `is_incremental: true`
- **AND** response includes `parent_commit_sha` field

#### Scenario: Get full conversation (default)
- **WHEN** client requests `GET /api/commits/<sha>` without incremental parameter
- **THEN** response includes full transcript (backward compatible)
- **AND** response includes `is_incremental: false`

#### Scenario: Incremental with no parent conversation
- **WHEN** client requests `GET /api/commits/<sha>?incremental=true`
- **AND** no parent commits have stored conversations
- **THEN** response includes full transcript
- **AND** response includes `is_incremental: false`
- **AND** `parent_commit_sha` is empty

### Requirement: Response Schema Extension
The conversation response SHALL include incremental display metadata.

#### Schema: ConversationResponse
```json
{
  "sha": "string",
  "session_id": "string",
  "timestamp": "string",
  "message_count": "number",
  "transcript": "array",
  "is_incremental": "boolean",
  "parent_commit_sha": "string (optional)",
  "incremental_count": "number (entries in this increment)"
}
```

### Requirement: UI Toggle
The web UI SHOULD provide a toggle to switch between incremental and full view.

#### Scenario: Toggle incremental/full
- **WHEN** user views a conversation in the web UI
- **THEN** a toggle is available to switch between incremental and full view
- **AND** incremental is the default when parent conversation exists
