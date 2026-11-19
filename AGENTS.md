# AGENTS.md

This file is for coding agents, and for anyone driving git-mop from a script. The first half covers working on the code. The second half is the JSON contract an agent relies on when it runs git-mop.

## What git-mop does

git-mop finds every git repository under a folder and decides, for each linked worktree and local branch, whether removing it would lose work. `git-mop` (scan) reports and saves a plan, `apply` removes what that plan lists after re-checking each item, `sync` fast-forwards branches that are only behind, and `where` finds the worktree a branch is checked out in.

The one rule everything else follows: **git-mop never deletes work that exists nowhere else.** When it can't tell, it keeps the item.

## Working on the code

### Layout

It's one Go package, `main`, at the repository root. That's the standard layout for a module that builds a single command. There are no third-party modules, so keep it that way unless a dependency removes real risk.

| File | What it owns |
| --- | --- |
| `main.go` | Command dispatch, flags, the apply and where documents and their renderers |
| `discover.go` | Finding repositories under the root |
| `scan.go` | Reading each repository: worktrees, branches, submodule work, `lsof` |
| `github.go` | PR lookups through `gh api graphql` |
| `classify.go` | Verdicts: whether each branch and worktree is safe, needs review or stays |
| `report.go` | Building the plan from verdicts, measuring sizes |
| `scanview.go` | The scan document (`--json`) and the scan report drawn from it |
| `plan.go` | Saving and loading the plan between scan and apply |
| `apply.go` | Removing planned items, re-checking each first, item IDs and `--only` |
| `sync.go` | Fast-forwarding branches, its document and renderer |
| `where.go` | Finding a branch across repositories |
| `ignored.go` | Which ignored files a build recreates and which would be lost |
| `keep.go` | Parsing the keep list |
| `git.go` | Running git: every call goes through `git()` or `gitIn()` |
| `output.go` | Colour, tables, the JSON header, error codes and exit codes |
| `types.go` | `Repo`, `Worktree`, `Branch`, `PR`, `Verdict` |

### Commands

```bash
go build -o git-mop .
go test ./...
go vet ./...
gofmt -l .
```

CI runs `gofmt`, `go vet` and `go test` on Ubuntu and macOS (`.github/workflows/ci.yml`). Tests need `git` on the PATH. They don't need network access, `gh` or a git identity.

### Rules for changes

- **Fail closed.** Any check that errors must keep the item: a failed `git status` counts as dirty, an unknown PR head counts as unsaved commits. Never write `out, _ :=` on a path that can lead to a removal unless an empty result keeps the item.
- **Re-check in apply.** Anything the scan checks before calling an item safe, `apply` checks again in `worktreeChanged` or `deleteBranch`, because the world moves between the two.
- **Don't trust git config to show you everything.** Use `statusIn()` rather than `git status` directly. It overrides `status.showUntrackedFiles` and `diff.ignoreSubmodules`.
- **Don't run code from a repository's own config.** `git()` and `gitIn()` turn off hooks and `core.fsmonitor`. Don't call `exec.Command("git", ...)` yourself.
- **No shell.** Pass arguments as arrays. Nothing a repository controls, such as a branch name, ever goes through a shell.
- **A safety fix comes with a regression test that fails before the fix.** Tests build real repositories in `t.TempDir()` with the helpers in `scan_test.go` (`fixture`, `sh`, `commit`) and `submodule_test.go` (`submoduleFixture`).
- **Test what a caller sees.** For apply that's its `[]applied` results, and for the CLI it's stdout, stderr and the exit code, using `runCLI` in `cli_test.go`. Don't assert on report text that's meant for people.

### Output rules

- Every command builds one document struct first. `--json` prints it, and the human renderer draws from it, so they can't disagree.
- In `--json` mode, stdout holds exactly one JSON document and nothing else, including on failure. Progress goes to stderr, and only when stderr is a terminal.
- Colour comes from `outStyle` and `errStyle`. It's on only for a terminal, `NO_COLOR` turns it off, and `CLICOLOR_FORCE` or `FORCE_COLOR` turn it on. Use `table` rather than `tabwriter`: tabwriter counts colour codes as width.
- Human text is plain and direct: say what happened and why, with no exclamation marks.

### Changing the JSON contract

- Adding a field or a reason code: fine at any time. Add new codes to the tables below.
- Removing or renaming a field, or changing what a field or code means: bump `schemaVersion` in `output.go` and say so in the commit message.
- Every verdict needs a code. The table tests in `classify_test.go` check that each case has one, so add a case for every new verdict.

## Running git-mop as an agent

### The workflow

1. **Scan:** `git-mop --json`, adding `--root <folder>` if the repositories aren't in `~/Developer`. Read `items`, where each one has an `action`:
   - `remove`: `apply` will remove it
   - `review`: a person should decide
   - `keep`: git-mop leaves it alone
   
   `attention` lists things git-mop never changes, such as stashes and commits that exist only on this machine.
2. **Decide:** check each `remove` item's `reason.code` against the table below. Ask the person before removing anything you're unsure about.
3. **Apply:** `git-mop apply --json` removes every `remove` item. `git-mop apply --only <id>,<id> --json` removes only the items you pass, and leaves the plan in place for the others. The plan expires 24 hours after the scan (`plan.expiresAt`).
4. **Check:** read `results`, where each entry's `outcome` is `removed`, `deleted`, `pruned`, `skipped` or `failed`. A skip means the item changed after the scan. `reason.code` says how, and nothing was removed.

Don't remove worktrees or branches yourself with `git worktree remove` or `git branch -D` based on the scan. `apply` re-checks each item first and logs a SHA for every removal.

### Every document

```json
{ "schemaVersion": 1, "command": "scan", "ok": true, ... }
```

On failure, `ok` is `false` and `error` holds a code and a message:

```json
{ "schemaVersion": 1, "command": "apply", "ok": false,
  "error": { "code": "plan_missing", "message": "no saved plan at ...: run git-mop first and review its report" } }
```

| Exit code | Meaning |
| --- | --- |
| 0 | Success |
| 1 | The command failed: read `error.code` |
| 2 | Bad flags or arguments (`error.code` is `usage` or `unknown_command`) |
| 3 | `where` found no worktree with the branch. The document is still `ok: true`. |

Error codes:

| Code | Meaning |
| --- | --- |
| `usage` | Bad flags or arguments |
| `unknown_command` | No such command |
| `root_not_found` | `--root` can't be read |
| `keep_file_invalid` | The keep list has a line git-mop doesn't understand |
| `lsof_missing` | `lsof` isn't installed |
| `lsof_failed` | `lsof` listed no processes |
| `plan_missing` | No saved plan. Run a scan first. |
| `plan_expired` | The plan is older than `--max-age`. Scan again. |
| `plan_invalid` | The plan file isn't valid JSON |
| `plan_write_failed` | The scan couldn't save its plan |
| `log_write_failed` | `apply` couldn't open its removal log, so it removed nothing |
| `unknown_id` | `apply --only` was given an ID the plan doesn't hold, so nothing was removed |
| `error` | Anything else |

### Scan document

`git-mop --json` prints:

- `root`: the folder that was scanned.
- `plan`: `path`, `createdAt` and `expiresAt` for the saved plan.
- `summary`: counts for `repos`, `remove`, `review`, `keep` and `attention`, plus `removeSizeBytes`.
- `items`: one entry per decision. The `remove` items come first, then `review`, then `keep`.
- `attention`: notices with `code`, `text`, `repo`, and `branch` and `count` where they apply.
- `next`: the commands to run next.

Each item has:

| Field | Meaning |
| --- | --- |
| `id` | Pass to `apply --only`. Stable between a scan and its apply. Treat it as opaque. |
| `kind` | `worktree`, `branch` or `stale_worktree` (all of a repository's worktree entries whose folders are gone) |
| `action` | `remove`, `review` or `keep` |
| `reason` | `{ "code": ..., "text": ... }` |
| `repo`, `repoPath`, `slug` | Repository name, its main checkout, and its GitHub `owner/name` |
| `branch`, `path`, `commit` | The branch, the worktree, and the branch tip or detached HEAD |
| `deletesBranch` | For a worktree to remove: whether its branch goes too |
| `sizeBytes` | Disk use of a worktree to remove, measured with `du` |
| `pr` | The PR that decided the branch: `number`, `state`, `base`, `url` |
| `uncommittedChanges`, `openIn`, `ignoredFiles` | What makes a worktree stay or need review |
| `commitsOnlyHere`, `commitsNotPushed` | Commits on no remote branch and in no PR GitHub keeps, and commits not on its upstream |
| `lastCommitAt` | When the branch tip was committed (RFC 3339) |

Fields that would be empty or zero are left out.

### Reason codes on scan items

| Code | Action | Meaning |
| --- | --- | --- |
| `in_default` | remove | Every commit is already in the default branch |
| `merged_pr` | remove | Its newest PR merged, and the branch has nothing beyond that PR's head |
| `closed_pr` | remove | Its newest PR closed with the branch at its head, which GitHub keeps |
| `throwaway` | remove | Matches a `throwaway` keep-list pattern |
| `detached_in_default` | remove | Detached worktree on a commit already in the default branch |
| `detached_on_remote` | remove | Detached worktree on a commit that's on a remote branch |
| `stale_worktree` | remove | Worktree entries whose folders are gone |
| `ignored_files` | review | Would be safe, but removing the worktree deletes ignored files nothing rebuilds, such as `.env` |
| `main_checkout_finished` | review | The main checkout is on a finished branch. Switch it first. |
| `keep_list` | keep | In the keep list |
| `long_lived` | keep | `main`, `develop` and the like, or a `longlived` keep-list entry |
| `open_pr` | keep | Has an open PR |
| `main_checkout` | keep | Checked out in the repository's main checkout |
| `default_branch` | keep | The default branch's worktree |
| `uncommitted_changes` | keep | The worktree has uncommitted changes, untracked files included |
| `submodule_work` | keep | A submodule holds commits on no remote branch, stashes, or ignored files that would be lost |
| `in_use` | keep | A process has its working directory inside the worktree |
| `nested_in_kept_worktree` | keep | Sits inside a linked worktree that stays |
| `local_only_commits` | keep | Has commits that exist only on this machine |
| `pushed_not_merged` | keep | Pushed, but not merged and no PR |
| `squash_merged` | keep | Looks squash-merged, but has no PR to prove it |
| `commits_beyond_merged_pr` | keep | Has commits added after its PR merged |
| `closed_pr_extra_commits` | keep | Its PR closed, but the branch has commits beyond it |
| `merged_pr_head_unknown` | keep | Its PR merged, but its head couldn't be compared |
| `stack_not_in_default` | keep | A stacked PR that merged into its base after the base had merged |
| `detached_local_only` | keep | Detached worktree on a commit that exists only on this machine |

### Attention codes

`stashes`, `default_branch_unpushed`, `main_checkout_dirty`, `local_only_commits`, `unpushed_commits`, `fetch_failed`, `github_failed`. A `github_failed` notice means that repository's branches were judged by git alone.

### Apply document

`git-mop apply --json` prints:

- `plan`: the plan that was applied.
- `root`: the folder its scan covered.
- `summary`: counts for `removed`, `deleted`, `pruned`, `skipped` and `failed`.
- `results`: one entry per item.
- `logPath`: the removal log. Every removal is in it with its SHA, so `git branch <name> <sha>` restores a branch.

Each result has `id`, `kind`, `outcome`, `repo`, `repoPath`, and `path`, `branch`, `commit` and `pruned` where they apply. A skipped or failed result also has a `reason`:

| Code | Meaning |
| --- | --- |
| `folder_gone` | The worktree folder no longer exists |
| `head_moved` | The worktree's HEAD moved since the scan |
| `branch_changed` | The worktree has a different branch checked out |
| `uncommitted_changes` | The worktree gained changes |
| `in_use` | A process opened the worktree |
| `ignored_files` | The worktree gained ignored files that would be lost |
| `submodule_work` | A submodule gained work that would be lost |
| `branch_gone` | The branch no longer exists |
| `branch_moved` | The branch has new commits since the scan |
| `checked_out` | The branch is checked out in a worktree |
| `git_failed` | git refused, and `text` has its message |

### Sync document

`git-mop sync --json` prints:

- `dryRun`: whether this was a `--dry-run`.
- `summary`: counts for `repos`, `updated`, `skipped`, `upToDate`, `fetchFailed`, `noUpstream`, `otherUpstream` and `detachedWorktrees`.
- `results`: one entry per branch that was behind, with `repo`, `branch`, `path`, `upstream`, `behind` and `action`. The `action` is `updated`, `would_update` or `skipped`.
- `fetchFailed`: one entry per failed fetch, with `repo`, `repoPath` and `error`.

A skipped branch has a reason code: `diverged`, `worktree_gone`, `status_unreadable`, `uncommitted_changes`, `in_use` or `git_failed`.

### Where document

`git-mop where <branch> --json` prints `pattern` and `matches`. Each match has `repo`, `repoPath`, `branch`, `path`, `main`, `state`, `stateText` and `uncommittedChanges`. The `state` is one of:

- `clean`
- `uncommitted_changes`
- `status_unreadable`
- `not_checked_out`
