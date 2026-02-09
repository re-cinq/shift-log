# claudit

Automatically save Claude Code conversations as Git Notes on every commit. Claude. Audit. See what we did there? 🥁

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/re-cinq/claudit/master/scripts/install.sh | bash
# ...In a Git repo
claudit init
```

Now work with `claude` as you would normally. Whenever you or Claude Code commit, the conversation since the last commit will be attached to that commit as a Git Note.

## Usage

**See what conversations you have:**

```bash
claudit list
```

**Resume a past session:**

```bash
claudit resume abc123    # By commit SHA
claudit resume HEAD~3    # By git ref
```

**View in your browser:**

````bash
claudit serve
```Han

**Pull down conversations from a repo you cloned:**

```bash
claudit sync pull
````

## Why?

In order to understand _how_ and _why_ a commit was made, we need to see the conversation that led to it.

## How It Works

Claudit uses [Git Notes](https://git-scm.com/docs/git-notes) to attach conversations to commits, stored under `refs/notes/claude-conversations` to keep `git log` clean. When you run `claudit init`, it sets up hooks so:

1. When Claude makes a commit, the conversation is saved automatically
2. When you make a commit during a Claude session, it's saved too
3. When you push/pull, conversations sync with the remote

No extra steps needed during your normal workflow.

To view notes directly with git: `git log --notes=claude-conversations`

## Commands

| Command                   | Description                            |
| ------------------------- | -------------------------------------- |
| `claudit init`            | Initialize claudit in the current repo |
| `claudit list`            | List commits with stored conversations |
| `claudit show [ref]`      | Show conversation history for a commit |
| `claudit resume <commit>` | Resume a Claude session from a commit  |
| `claudit serve`           | Start the web visualization server     |
| `claudit doctor`          | Diagnose claudit configuration issues  |
| `claudit debug`           | Toggle debug logging                   |
| `claudit sync push/pull`  | Sync conversation notes with remote    |
| `claudit remap`           | Remap orphaned notes to rebased commits|

## Requirements

- Git
- Claude Code CLI (for resume)

## Multi-Developer Sync

When multiple developers use claudit on the same repository, each person's conversation notes are synced automatically via git push/pull hooks. Notes from different developers are merged seamlessly because they typically annotate different commits.

If the remote notes ref has diverged (e.g. two developers pushed notes without pulling first), `claudit sync push` will reject the push and advise you to pull first:

```bash
claudit sync pull   # Fetches and merges remote notes
claudit sync push   # Now succeeds
```

In the rare case where two developers annotate the exact same commit SHA, both notes are preserved by concatenation — no data is lost.

## Local Rebase

Conversation notes automatically follow commits when you rebase. During `claudit init`, the `notes.rewriteRef` git config is set to `refs/notes/claude-conversations`, which tells git to remap notes to the new commit SHAs during rebase. No manual steps are needed.

If you initialized before this config was added, run `claudit init` again or set it manually:

```bash
git config notes.rewriteRef refs/notes/claude-conversations
```

You can verify the config is set with `claudit doctor`.

## GitHub Rebase Merge

When a PR is merged via GitHub's "Rebase and merge" strategy, the commits get new SHAs on the target branch. Since this happens server-side, git's `notes.rewriteRef` does not apply and notes remain keyed to the original (now-orphaned) commit SHAs.

Claudit handles this automatically. After you pull a rebase-merged PR, the post-merge hook runs `claudit remap`, which:

1. Finds notes on commits that are no longer on any branch (orphaned)
2. Uses `git patch-id` to match each orphaned commit to its rebased counterpart by diff content
3. Copies the note to the new commit SHA

You can also run it manually:

```bash
claudit remap
```

This reports how many notes were remapped and how many remain unmatched. Notes that cannot be matched (e.g. if the original commit was garbage collected) are left in place and reported but not deleted.

## License

[AI Native Application License (AINAL)](https://github.com/re-cinq/ai-native-application-license) — see [LICENSE](LICENSE).
