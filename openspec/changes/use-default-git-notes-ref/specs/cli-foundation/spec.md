# CLI Foundation

Delta: Dynamic git notes ref selection with user configuration.

## ADDED Requirements

### Requirement: Configuration management
Claudit MUST persist user configuration in `.claudit/config`.

#### Scenario: Read configuration
- Given a `.claudit/config` file exists
- When claudit reads the configuration
- Then it loads the stored settings including notes_ref

#### Scenario: Write configuration
- Given claudit needs to store configuration
- When writing configuration
- Then it creates `.claudit/config` with JSON content
- And ensures the `.claudit/` directory exists

#### Scenario: Default configuration when missing
- Given `.claudit/config` does not exist
- When claudit reads configuration
- Then it returns default settings (notes_ref = "refs/notes/commits")

### Requirement: Notes ref selection during init
The `claudit init` command MUST prompt users to choose their git notes ref strategy.

#### Scenario: Interactive ref selection
- Given the user runs `claudit init`
- And `.claudit/config` does not exist
- When init runs
- Then claudit prompts "Which git notes ref should claudit use?"
- And offers "refs/notes/commits (default)" as first option
- And offers "refs/notes/claude-conversations (custom)" as second option

#### Scenario: Store ref choice
- Given the user selects a notes ref during init
- When init completes
- Then the choice is stored in `.claudit/config`
- And the chosen ref is used for all subsequent operations

#### Scenario: Non-interactive ref selection
- Given the user runs `claudit init --notes-ref=refs/notes/commits`
- When init runs
- Then claudit skips the prompt
- And stores the provided ref in `.claudit/config`

#### Scenario: Reuse existing configuration
- Given `.claudit/config` already exists with a notes ref
- When the user runs `claudit init`
- Then claudit reuses the existing ref choice
- And does not prompt again

### Requirement: Git configuration for chosen ref
The init command MUST configure git settings for the chosen notes ref.

#### Scenario: Configure displayRef
- Given `claudit init` completes with ref choice
- When checking git config
- Then `notes.displayRef` is set to the chosen ref
- So `git log` displays notes from that ref

#### Scenario: Configure rewriteRef
- Given `claudit init` completes with ref choice
- When checking git config
- Then `notes.rewriteRef` is set to the chosen ref
- So notes follow commits during rebase/amend

## MODIFIED Requirements

### Requirement: Sync commands
The CLI MUST sync git notes using the configured ref.

#### Scenario: Push notes to remote
- Given the repository has a remote configured
- And `.claudit/config` specifies a notes ref
- When the user runs `claudit sync push`
- Then claudit pushes the configured ref to origin

#### Scenario: Pull notes from remote
- Given the repository has a remote configured
- And `.claudit/config` specifies a notes ref
- When the user runs `claudit sync pull`
- Then claudit fetches the configured ref from origin
