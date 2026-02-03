# Proposal: Use Default Git Notes Ref with Optional Custom Ref

## Problem

Currently, claudit stores conversation notes in `refs/notes/claude-conversations`, requiring users to specify `--ref=refs/notes/claude-conversations` for all git notes commands:

```bash
# Current situation - verbose and non-standard
git notes --ref=refs/notes/claude-conversations show HEAD
git log --show-notes=refs/notes/claude-conversations
```

This custom ref was chosen for namespace separation to avoid collisions, but creates poor ergonomics:
- Standard `git notes` commands don't work without the ref flag
- Notes are hidden from `git log` by default
- Notes don't follow commits during rebases unless explicitly configured
- Developers must remember the custom ref path for all operations

## Proposed Solution

Use the git default notes ref (`refs/notes/commits`) by default, with an optional configuration to use a custom ref when the user explicitly wants namespace separation.

During `claudit init`, prompt the user to choose their preferred ref strategy:

```
? Which git notes ref should claudit use?
  > refs/notes/commits (default - works with standard git commands)
    refs/notes/claude-conversations (custom - separate namespace)
```

Store the choice in `.claudit/config` and use it consistently across all operations.

## Benefits

**For default ref users (expected majority):**
- Standard git commands work: `git notes show HEAD`, `git log` (with displayRef config)
- Notes follow commits during rebases/cherry-picks (with rewriteRef config)
- Better integration with git workflows and tooling
- One less thing to remember

**For custom ref users:**
- Explicit namespace separation from other git notes
- No collision risk with existing note-using tools
- Clear ownership of the ref

## Non-Goals

- Automatic migration from custom to default ref (no existing users yet)
- Runtime ref switching (choice made during init, persists)
- Supporting multiple refs simultaneously (single source of truth per repo)

## Trade-offs

| Aspect | Default Ref | Custom Ref |
|--------|-------------|------------|
| Ergonomics | Excellent (standard commands work) | Poor (requires --ref flag) |
| Collision risk | Low (unlikely other tools use it) | None |
| Visibility | High (git log shows notes) | Low (hidden by default) |
| Rebase behavior | Follows commits (with config) | Requires manual config |

## Implementation Approach

1. Add configuration management (`.claudit/config` JSON file)
2. Update `claudit init` to prompt for ref choice and store in config
3. Add `GetNotesRef()` function that reads from config, defaults to `refs/notes/commits`
4. Replace hardcoded `NotesRef` constant with `GetNotesRef()` calls
5. Configure git settings (`notes.displayRef`, `notes.rewriteRef`) during init for chosen ref
6. Update tests to cover both ref strategies
7. Update documentation to explain the choice and implications

## Acceptance Criteria

- [ ] Users can choose between default and custom ref during `claudit init`
- [ ] Configuration persists in `.claudit/config` and is respected by all commands
- [ ] For default ref: `git notes show HEAD` works without --ref flag
- [ ] For default ref: `git log` shows notes (via notes.displayRef config)
- [ ] For custom ref: behavior unchanged from current implementation
- [ ] Acceptance tests cover both ref strategies
- [ ] Documentation updated with ref choice explanation
