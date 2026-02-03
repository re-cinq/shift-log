# Implementation Tasks

## Phase 1: Configuration Infrastructure
- [ ] Create `internal/config` package with Config struct
- [ ] Implement `ReadConfig()` to load from `.claudit/config`
- [ ] Implement `WriteConfig()` to save configuration
- [ ] Add `NotesRef` field to config (defaults to `refs/notes/commits`)
- [ ] Unit tests for config read/write

## Phase 2: Init Command Enhancement
- [ ] Add interactive prompt to `claudit init` for ref choice
- [ ] Store user's choice in `.claudit/config`
- [ ] Configure `notes.displayRef` based on chosen ref
- [ ] Configure `notes.rewriteRef` based on chosen ref
- [ ] Update init success message to show chosen ref
- [ ] Add `--notes-ref` flag for non-interactive init

## Phase 3: Notes Package Refactoring
- [ ] Add `GetNotesRef()` function that reads from config
- [ ] Replace `NotesRef` constant with `GetNotesRef()` calls in all functions
- [ ] Add fallback to default ref if config doesn't exist (backwards compat)
- [ ] Update `PushNotes()` and `FetchNotes()` to use dynamic ref

## Phase 4: Testing
- [ ] Acceptance test: init with default ref choice
- [ ] Acceptance test: init with custom ref choice
- [ ] Acceptance test: store/list/resume with default ref
- [ ] Acceptance test: store/list/resume with custom ref
- [ ] Acceptance test: verify git config settings (displayRef, rewriteRef)
- [ ] Update existing tests to handle both ref strategies
- [ ] Integration test: verify standard `git notes show HEAD` works with default ref

## Phase 5: Documentation
- [ ] Update README with ref choice explanation
- [ ] Update `claudit init` command documentation
- [ ] Add FAQ entry about choosing between refs
- [ ] Update `openspec/project.md` to reflect new default

## Validation
- [ ] Run `openspec validate use-default-git-notes-ref --strict`
- [ ] All acceptance tests pass with both ref configurations
- [ ] Manual test: `git notes show HEAD` works after init with default ref
- [ ] Manual test: `git log` shows notes after init with default ref
