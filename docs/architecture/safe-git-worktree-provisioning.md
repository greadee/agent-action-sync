# Safe Git worktree provisioning

- Status: implemented for Phase 1 Slice 4
- Scope: authority-local Git branches and worktrees for supervised attempts

`workspace.GitWorktreeManager` is the first filesystem-mutating orchestration
adapter. It provisions a deterministic `syncgate/attempt-<digest>` branch and
one hashed worktree directory for an exact attempt. Construction pins the
project root, repository root, worktree base, Git executable, and an explicit
allowlist of base commit hashes. Allocation rechecks the clean primary
worktree, branch and target collisions, real non-symlink roots, Git metadata,
disk budget, and the requested base commit before calling Git.

The worktree base must be outside both synchronized project content and the
repository. The adapter never checks out, resets, cleans, merges, or changes
files in the primary worktree. Branch creation is the only repository-level
mutation. There is no automatic merge path.

An authority-local registry under the external worktree base retains the
absolute worktree path and ownership evidence. Public workspace values contain
only opaque IDs, owner, deterministic branch, generation, state, and base/head
commit hashes. Restart reconciliation marks missing allocated worktrees as
`quarantined`; it does not recreate or delete them by inference.

Cleanup requires the exact workspace owner and generation. A clean worktree
may be removed through `git worktree remove`; its branch and registry evidence
remain. A dirty worktree is moved logically to `quarantined` and kept for
operator inspection. No recursive-force deletion is implemented.

Change inspection combines committed changes since the pinned base with
uncommitted and untracked changes, emits a deterministic digest-bearing file
manifest, rejects symlinks/non-regular files, and reapplies the execution
contract's write scope to every relative path. `.git` paths are always denied.
The manifest contains no absolute path or file bytes.
