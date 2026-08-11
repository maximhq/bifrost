---
name: stack-absorb
description: Manually distribute working-tree changes (or a batch of edits already made on the current branch) across the correct branches of a Graphite (gt) stack, when gt absorb's blame-based auto-split doesn't match the logical grouping - e.g. new code with no prior line to blame onto, or changes that conceptually belong with a commit gt's algorithm wouldn't pick. Use when asked to "split these changes into the stack", "absorb this properly", "put this fix on the right branch", or when a change was made on top of a branch but really belongs several branches down.
allowed-tools: Read, Grep, Glob, Bash, Edit, AskUserQuestion
---

# Stack Absorb

Distribute a set of changes across the correct commits in a Graphite stack, by hand,
when `gt absorb`'s blame-based algorithm won't produce a clean result. This is slower
than `gt absorb -f` but gives a coherent, reviewable history: each target branch gets
exactly the hunks that belong to it, described in its own commit message, with nothing
stray swept in.

## When to reach for this vs. plain `gt absorb`

Check `git status --short` first. Untracked (`??`) files are never absorbed even with
`gt absorb -a` - that flag only stages unstaged *tracked* changes, file creations are
never picked up. If any untracked files are part of the intended change, treat them as
part of the manual split below rather than expecting the dry-run to cover them.

Then try `gt absorb -a --dry-run` first - it's free and sometimes it's exactly right.
Read its output critically:

- If every hunk absorbs into a commit that's a good semantic fit, there's no
  significant "Not absorbed" leftover, and no untracked files were left out of
  consideration, just run `gt absorb -a -f` (or let the user run it) and stop here.
- If it scatters hunks across commits that are a poor conceptual fit (a stale
  docs-sync commit, a commit about a different concern that happens to touch the same
  lines), or leaves substantial new code as "Not absorbed" (no prior line to blame
  onto - new functions, new JSX blocks, new state), that's the signal to do this
  manually. Tell the user what `gt absorb --dry-run` would do and why it's messier
  than a hand split, and confirm before proceeding (`AskUserQuestion`) - this is a
  judgment call about commit history shape, not a mechanical one.

## Step 1: Map every change to its target branch

For each distinct concern in the diff, find the commit that should own it:

```bash
git log --oneline -S"<distinctive string from the change>" --all -- "<file>"
```

Pick a string that's specific to the *concept* being changed (a function name, a UI
label, an error message), not boilerplate. The oldest/most relevant match is usually
the commit that introduced what you're now modifying. Then resolve that commit to a
current branch tip - `git branch --contains` alone is not the answer, only a candidate
list: in a stacked history every branch *above* the true owner also contains that
commit, since it's in their ancestry too.

```bash
git branch --contains "<hash>"    # candidates: the owner + everything upstack of it
git branch --points-at "<hash>"   # non-empty only if <hash> IS a branch tip itself
gt log long                       # see the full stack shape with commit messages
```

If `--points-at` returns a branch, that's the owner. Otherwise, treat `--contains`'s
output as candidates only, cross-check against `gt log long` for which branch's own
commits plausibly introduce the concept, and record which one you picked. If more than
one candidate still looks plausible, stop and ask the user (`AskUserQuestion`) rather
than guessing.

Group the diff's hunks by target branch. A single file's changes routinely split
across two or more branches (e.g. a bugfix on an old branch, a rename + new feature on
a newer one) - that's expected, not a problem.

**Judgment, not blame**: the "right" branch is the one that owns the concept being
touched, which is not always the same as the last branch to edit that exact line. A
generated file (e.g. a bundled `openapi.json`) belongs with whichever branch changed
its source, not with whatever branch happens to have last regenerated it.

If a target file/section doesn't exist yet on the candidate branch (check with
`git show "<branch>:<path>"`), the change belongs on a *later* branch - find the commit
that actually introduces that file/section instead.

## Step 2: Back up, then return to clean HEAD

Before touching anything, copy the final intended content of every changed file
somewhere safe (the session scratchpad), preserving each file's relative path so two
files with the same basename in different directories don't clobber each other's
backup. Abort before touching the working tree if any backup fails:

```bash
mkdir -p "<scratchpad>/$(dirname "<file>")" && cp "<file>" "<scratchpad>/<file>"   # for each modified file
```

Then restore tracked files to the current branch's committed state. Set aside any
intended untracked (newly-created) files separately first, since they have no
committed state to restore to and `git restore`/`git checkout` won't touch them:

```bash
git status --short                                                   # confirm what's dirty first
git restore --source=HEAD --staged --worktree -- "<tracked-files>"   # NOT git checkout --
                                                                       # (checkout only resets the
                                                                       # worktree from the index,
                                                                       # not the index itself)
                                                                       # NOT git stash - never use
                                                                       # git stash in this repo
git status --short                                                   # confirm the tree is clean again
```

Never `git reset --hard` or touch files you didn't back up. If `git status` shows
files you don't recognize as part of this change, stop and ask the user before doing
anything - don't assume it's safe to revert or sweep in.

## Step 3: Apply each branch's slice, bottom-up

Process target branches from the bottom of the stack upward (earliest commit first) -
this matters because later branches' restacks will replay onto whatever the earlier
branch ends up containing.

For each target branch:

1. `gt checkout <branch>` and confirm `git status --short` is clean before editing.
2. Read the current content of each file at this branch's tip - it usually differs
   from the top-of-stack version (later branches may have renamed things, added
   props, etc.), so re-derive the edit against what's actually here rather than
   blindly replaying the final diff. Verify the "before" text matches what you expect
   before editing.
3. Apply only this branch's slice of the change.
4. Verify: run the appropriate build/typecheck for whatever you touched (`tsc
   --noEmit`, `go build ./...`, etc.) before committing anything.
5. Check `git status --short` again - it must show *only* the files you intended to
   touch. If unrelated files are dirty (leftover debug instrumentation, a stray edit
   from elsewhere), do not sweep them in with `-a`; stage the intended files
   explicitly, or ask the user what to do with the stray diff first.
6. Commit with `gt modify -a -m "<message>"` (amends the branch's existing commit and
   auto-restacks everything above it). Use `gt modify -c` instead if this should be a
   *new* commit on the branch rather than folded into the existing one. Never use
   plain `git commit` here - it won't restack descendants.
7. Before running `gt modify`, regenerate any derived/bundled output that must stay in
   sync with the source you just edited - `gt modify` itself does not know about
   repository-specific generators. For OpenAPI edits, run `cd docs/openapi && python3
   bundle.py`, then confirm `git status --short` lists both the edited source YAML and
   `docs/openapi/openapi.json` before committing, so the generated file lands in the
   same commit as its source.

## Step 4: Resolve restack conflicts

A restack conflict is expected whenever another stacked branch independently touched
the same lines (e.g. two different features both added a prop to the same component).
`gt modify`/`gt restack` will stop and report it:

```
Hit conflict restacking <branch> on <parent>.
Unmerged files: <path>
```

To resolve:

1. Stay on the conflicted branch with the operation paused - don't `gt checkout` away
   or start a new `gt restack`; the conflict markers are already sitting in the working
   tree from the operation that just stopped. `gt abort` only cancels the paused
   operation entirely, it is not a way to "restart" it.
2. `grep -n "^<<<<<<<\|^=======\|^>>>>>>>" "<file>"` to locate every conflict region in
   the file - there is often more than one.
3. Resolve each region by hand. The common case in this workflow is "keep both sides"
   (two independent additions near the same spot) - merge them in a sensible order
   rather than picking one side and discarding the other.
4. Verify the file typechecks/builds after resolving, with no markers left:
   `grep -cE "^(<<<<<<<|=======|>>>>>>>)" "<file>"` should be 0.
5. `gt add "<file>"` for every resolved file, then `gt continue`.
6. Repeat if `gt continue` hits another conflict further up the stack.

## Step 5: Final verification

After the last branch is committed and restacked:

```bash
gt log short                                                  # no "(needs restack)" on any branch in your chain
grep -rlE '^(<<<<<<<|=======|>>>>>>>)' -- "<touched dirs>"     # must be empty
git status --short                                             # clean
```

Verify from the *highest* branch your changes touched or that got restacked as a
result (not necessarily the branch the user started on) - restacking replays every
commit above your changes, and a downstream branch could in principle break even
without a conflict (e.g. a type that was fine in isolation becomes wrong once combined
with a later branch's edits). If independent branches upstack diverged from each other,
run the full build/typecheck from each affected branch's tip. Only after that, return
to whatever branch the user was originally on.

Report back per-branch: which branch got what, and the verification status of each.
Do not push anything - this workflow only rewrites local branches.

## Guardrails

- Never `git stash` in this repo (see project convention: toolchain/lockfile churn
  makes pops conflict).
- Never include unrelated dirty files in a `gt modify -a` - confirm `git status
  --short` shows only the intended paths first, every time, even if it was clean a
  moment ago (checking out a different branch can surface a different set of stray
  changes).
- Never invent commit content: if a file/section a change should modify doesn't exist
  yet at the candidate branch, that's a sign you picked the wrong branch, not a reason
  to create it early.
- Confirm the overall plan with the user before executing when the mapping required
  real judgment (not purely mechanical) - name the target branch for each file/concern
  and get a go-ahead before checking out and editing anything.
