# Phase 10: Desktop Multi-Project Runtime Authority

Status: accepted

## Context

The desktop node can discover and project several Agent Projects, while the
Slice 3 runtime composition owns one Git repository, one worktree allocator,
one scheduler, and one node lease ceiling. Treating a project ID on a scheduler
request as runtime authority would let any registered project redirect that
shared composition to the repository authorized during execution preflight.

## Decision

Project registration remains a portable-manifest discovery concern. The local
operations surface reads that bounded registration inventory but returns only
project ID, display metadata, selection and policy state, aggregate assignment
and gate counts, and scheduler state. It never returns a registration root,
share ID, workspace path, runtime session, prompt, content, or artifact bytes.

The node stores policy and selection in two authority-local SQLite tables.
There is exactly one selection row. A project policy controls whether scheduling
is allowed and sets a one-or-two attempt concurrency ceiling no higher than the
node authorization. Policy changes and project switches require a paused
scheduler. The selected ceiling is installed before scheduler resume.

Runtime authority is separately bound to the registered project whose canonical
root matches the root revalidated by desktop execution preflight. Scheduler
start, assignment mutation, and integration decisions require all three facts:

1. the project is registered;
2. the project is the explicit local selection; and
3. the project is the execution-authorized registration.

The polling source also drops requests for every other project. Registering or
selecting another project therefore grants inventory access only; execution
requires a new stopped-node execution authorization and runtime composition for
that project's root.

## Consequences

One desktop node can inspect several projects without sharing runtime authority.
Switching projects is deliberate and fail-closed. A project name and aggregate
counts remain local operator metadata, while paths and content stay behind the
authority boundary.

Selection, policy, assignments, leases, projections, and runtime handles are
not portable history. They may be included in a same-node cold disaster backup,
but they are never restored onto another node as execution authority. Portable
project records are restored first; the target node then rebuilds projections,
creates fresh local policy and selection, and repeats credential and execution
authorization.

## Verification

Tests cover exclusive durable selection, project-scoped assignment and gate
aggregates, bounded pagination, path-free API output, blocked switching while
running, blocked scheduler start for a selected but unauthorized project,
project concurrency installation while paused, and source filtering by the
execution-authorized project.
