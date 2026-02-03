# Conversation Storage

Delta: Use dynamically configured git notes ref instead of hardcoded custom ref.

## MODIFIED Requirements

### Requirement: Store as Git Note
The compressed transcript MUST be stored as a Git Note attached to the commit using the configured ref.

#### Scenario: Create git note on configured ref
- Given a compressed, encoded transcript
- And `.claudit/config` specifies a notes ref
- When storing the conversation
- Then claudit attaches the note to HEAD using the configured ref

#### Scenario: Use default ref when config missing
- Given a compressed, encoded transcript
- And `.claudit/config` does not exist
- When storing the conversation
- Then claudit attaches the note using `refs/notes/commits` (default)

### Requirement: Read notes from configured ref
Note retrieval operations MUST use the configured ref.

#### Scenario: List commits with notes
- Given commits have notes on the configured ref
- When running `claudit list`
- Then claudit reads notes from the configured ref

#### Scenario: Resume from commit
- Given a commit has a note on the configured ref
- When running `claudit resume <commit>`
- Then claudit reads the note from the configured ref
- And restores the conversation
