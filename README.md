# git-mop

Finds every git repository under a folder and reports which worktrees and branches can go without losing work, and what needs attention: uncommitted changes, unpushed commits, stashes and stale worktree entries. `apply` removes what the report offered. `sync` fast-forwards branches that are only behind, and `where` finds the worktree a branch is checked out in.

It never deletes anything when it reports. Removal happens only in `apply`, only for the items the last report listed, and each one is checked again first.

## Install

```bash
go install github.com/tomheaton/git-mop@latest
```

This needs Go 1.27 or later. It runs on macOS and Linux and needs `git`, `lsof`, and an authenticated [`gh`](https://cli.github.com) for PR lookups (`--no-github` skips them). With the binary on your `PATH`, git also runs it as `git mop`.

## Usage

It looks under `~/Developer` by default. Pass `--root` to search somewhere else.

```bash
git-mop                       # report on ~/Developer and save the plan
git-mop --root ~/code         # report on another folder
git-mop apply                 # remove what that report listed under SAFE TO REMOVE
git-mop sync --dry-run        # show which branches would fast-forward
git-mop sync                  # fetch, then fast-forward them
git-mop where fix/login       # which worktree has it checked out
cd "$(git-mop where fix/login --repo api --path)"
```

A report looks like this:

```
git-mop: 1 repos under ~/Developer

SAFE TO REMOVE  (git-mop apply)

  Worktrees: 1, 8K by du (pnpm installs share files, so less is freed)
    api-fix-login  fix/login  already in main  8K

  Branches with no worktree: 1
    api  old/spike  already in main

NEEDS ATTENTION  (never changed)
  api  1 stash
  api  1 branch with commits only on this machine

KEPT: 2
     1  no PR, commit only on this machine
     1  worktree has uncommitted change
  (-v lists them)

nothing removed: run git-mop apply to remove the items under SAFE TO REMOVE
```

## What counts as safe

A branch is safe when every commit on it lives somewhere that outlasts it:

- it's already in the default branch
- its newest PR is merged and the branch has nothing beyond that PR's last head
- its newest PR is closed and the branch has nothing beyond its head, which GitHub keeps

A merged PR in a stack only counts when it reached the default branch. If it merged into its base after that base had merged, the default branch must have its own `(#N)` commit for it.

A worktree is safe when its branch is, it has no uncommitted changes, no process has its working directory inside it, it holds no ignored files that would be lost, and it isn't inside a linked worktree that stays.

Removing a worktree deletes its ignored files too, and `git status` doesn't show them. Anything that an install or build recreates (`node_modules`, `dist`, `.next`, caches, `.husky/_` and the like) doesn't count. Anything else, such as `.env` or `.claude/notes/`, moves the worktree to review with the files named. Mark files that are fine to lose with `ignorable` in the keep list. A detached worktree is safe when its commit is in the default branch or on a remote branch.

Kept whatever their state: open PRs, long-lived branches (`main`, `master`, `develop`, `staging`, `production`, plus any `longlived` entries in the keep list), main checkouts, and anything in the keep list. A main checkout sitting on a merged branch is listed under review, since it needs switching first.

## Keep list

`~/.config/git-mop/keep`, one entry per line. Lines starting with `#` are comments.

```
# keep this branch in the repository named api
api fix/login
# keep a worktree, or everything under a folder
path ~/Developer/client/legacy-site
# branches whose local-only commits don't need keeping, such as release cuts that are never pushed
throwaway api release/*
# ignored files that are fine to lose with a worktree
ignorable .claude/settings.local.json
# a branch, in any repository, that PRs merge into and that is never deleted
longlived next
```

## Applying

Each scan saves what it offered to `~/.local/state/git-mop/plan.json`. `git-mop apply` removes only the items in that plan, so nothing that became safe after you read the report goes without you seeing it. It refuses a plan older than 24 hours (`--max-age`), and a plan is used once.

It checks each item again just before removing it and skips anything whose HEAD moved, branch changed, or that gained changes, ignored files or a process since the scan. Every removal goes to `~/.local/state/git-mop/removed.log` with its SHA, so a branch comes back with `git branch <name> <sha>`.

It never deletes whole clones, stashes, or anything with uncommitted changes.

## Syncing

`sync` fetches every repository, then fast-forwards each local branch that is behind its own remote branch and has no commits of its own. A checked-out branch moves only when its worktree has no tracked changes and no process open in it (`--include-open` to update those too). It never merges, rebases or resets. Branches that track a different branch, such as a feature branch cut from `origin/master`, are left alone. Diverged branches and worktrees with edits are listed with the reason.

## Finding a branch

`where <branch>` lists every worktree with the branch checked out and whether it's clean, plus repositories where the branch exists without a worktree. It takes a glob (`'fix/*'`), `--repo` to pick a repository and `--path` to print paths only. It exits 1 when no worktree has the branch.

## Flags

Scan: `--root`, `--depth`, `--jobs`, `--no-fetch`, `--no-github`, `--no-sizes`, `--json`, `-v` (list kept items), `--keep`, `--plan`.

Apply: `--plan`, `--max-age`, `--keep`, `--log`, `--json`.

Sync: `--root`, `--depth`, `--jobs`, `--no-fetch`, `--dry-run`, `--include-open`, `--json`.

Where: `--root`, `--depth`, `--repo`, `--path`, `--json`.

Every command takes `--json` for scripts and agents, and `-h` for its flags.

## Development

```bash
go build -o git-mop .
go test ./...
```

## License

MIT
