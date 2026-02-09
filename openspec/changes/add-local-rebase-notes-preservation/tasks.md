## 1. Implementation
- [ ] 1.1 Verify `claudit init` sets `notes.rewriteRef` to `refs/notes/claude-conversations` (already done in `cmd/init.go:165`)
- [ ] 1.2 Add `notes.rewriteRef` check to `claudit doctor` output
- [ ] 1.3 Add "Local Rebase" section to README.md explaining automatic note preservation

## 2. Testing
- [ ] 2.1 Acceptance test: notes follow commits after `git rebase` when `notes.rewriteRef` is set
- [ ] 2.2 Acceptance test: `claudit doctor` reports OK for `notes.rewriteRef` when configured
- [ ] 2.3 Acceptance test: `claudit doctor` reports FAIL for `notes.rewriteRef` when missing
