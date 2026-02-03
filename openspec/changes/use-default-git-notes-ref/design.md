# Design: Dynamic Git Notes Ref Selection

## Architecture

### Configuration Storage

```
.claudit/
  config        # JSON config file (not checked into git)
```

Config schema:
```json
{
  "notes_ref": "refs/notes/commits"  // or "refs/notes/claude-conversations"
}
```

**Rationale:** Store in `.claudit/config` (not `.claudit/config.json`) to mirror Claude Code's `.claude/settings.local.json` pattern. Not checked into git to allow per-developer preferences in shared repos.

### Configuration Access Pattern

```go
// internal/config/config.go
type Config struct {
    NotesRef string `json:"notes_ref"`
}

const DefaultNotesRef = "refs/notes/commits"
const CustomNotesRef = "refs/notes/claude-conversations"

func Read() (*Config, error) {
    // Read from .claudit/config
    // Return defaults if file doesn't exist
}

func Write(cfg *Config) error {
    // Write to .claudit/config
    // Create .claudit/ directory if needed
}
```

### Notes Package Integration

```go
// internal/git/notes.go
func GetNotesRef() string {
    cfg, err := config.Read()
    if err != nil {
        // Fallback to default for new repos or errors
        return config.DefaultNotesRef
    }
    return cfg.NotesRef
}

func AddNote(commitSHA string, content []byte) error {
    ref := GetNotesRef()
    cmd := exec.Command("git", "notes", "--ref", ref, "add", "-f", "-m", string(content), commitSHA)
    return cmd.Run()
}
```

**Rationale:** Centralize ref resolution in `GetNotesRef()`. All git notes operations call this function instead of using a constant. Allows runtime configuration without globals.

## Init Flow

```
claudit init
  ↓
Check if .claudit/config exists
  ↓
If exists: read and reuse NotesRef
If not: prompt user for choice
  ↓
Store choice in .claudit/config
  ↓
Configure git settings based on choice
  - git config notes.displayRef <chosen-ref>
  - git config notes.rewriteRef <chosen-ref>
  ↓
Install hooks
```

### Non-interactive Mode

Support `--notes-ref` flag for CI/automation:

```bash
claudit init --notes-ref=refs/notes/commits
claudit init --notes-ref=refs/notes/claude-conversations
```

## Git Configuration

For the chosen ref, configure:

1. **notes.displayRef** - Makes `git log` show notes from this ref
2. **notes.rewriteRef** - Makes notes follow commits during rebase/amend

```bash
git config notes.displayRef refs/notes/commits
git config notes.rewriteRef refs/notes/commits
```

**Rationale:** These configs make the ref behave like the git default, solving the ergonomics problem even when using custom ref (though custom ref still requires --ref for manual commands).

## Backwards Compatibility

For repos initialized before this change (though none exist in production):

1. If `.claudit/config` doesn't exist, `GetNotesRef()` returns default ref
2. Existing notes on custom ref remain accessible via manual --ref flag
3. No automatic migration needed (no users affected)

## Testing Strategy

### Acceptance Tests

Test both ref strategies independently:

```go
Context("with default ref", func() {
    BeforeEach(func() {
        // Initialize with default ref
        RunClaudit("init", "--notes-ref=refs/notes/commits")
    })

    It("stores notes on default ref", func() {
        // Verify notes.HasNote works
        // Verify git notes show HEAD works
    })
})

Context("with custom ref", func() {
    BeforeEach(func() {
        // Initialize with custom ref
        RunClaudit("init", "--notes-ref=refs/notes/claude-conversations")
    })

    It("stores notes on custom ref", func() {
        // Verify notes.HasNote works
        // Verify git notes --ref=... show HEAD works
    })
})
```

### Test Utilities

Update `testutil.GitRepo` to accept ref parameter:

```go
func (r *GitRepo) HasNoteOnDefaultRef(commit string) bool {
    // Check default ref (no --ref needed)
    return r.HasNote("refs/notes/commits", commit)
}
```

## Migration Path (Future)

If we later need to migrate existing custom ref notes to default:

```bash
claudit migrate --from=refs/notes/claude-conversations --to=refs/notes/commits
```

This would:
1. Copy all notes from source ref to target ref
2. Update `.claudit/config`
3. Delete old ref (with confirmation)

**Not implementing now** since no users exist yet.

## Error Handling

| Scenario | Behavior |
|----------|----------|
| `.claudit/config` missing | Use default ref, create config on next write |
| `.claudit/config` malformed | Log warning, use default ref |
| Invalid ref in config | Validate during init, reject invalid refs |
| Git config fails | Log warning, continue (hooks will work, manual commands may not) |

## Alternative Considered: Environment Variable

Could use `CLAUDIT_NOTES_REF` env var instead of config file.

**Rejected because:**
- Requires setting env var in every shell session
- Harder to persist across team members
- Config file is more explicit and discoverable
- Mirrors Claude Code's local settings pattern
