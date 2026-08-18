---
name: split-commit-into-stack
description: Split one oversized commit or branch into a stack of independently reviewable Graphite (gt) PRs - deciding what genuinely separates, what is atomic and must stay whole, and proving each lower PR builds and passes without the ones above it. Use when asked to "split this PR", "this commit is too big", "break this into a stack", "can we separate this out", or when a diff bundles several concerns across different layers.
allowed-tools: Read, Grep, Glob, Bash, Edit, AskUserQuestion
---

# Split Commit Into Stack

Turn one large commit into a stack where each PR is a coherent story that compiles and
passes on its own. The work is mostly analysis: the mechanics are five commands, but
choosing the cut and proving it holds is where the value is.

Most of the cost of a big PR is that a reviewer cannot tell which changes belong to
which decision. A split earns its keep when it separates *decisions*, not when it
merely reduces line count.

## Step 0: Measure before proposing anything

```bash
git --no-pager diff --numstat HEAD~1 HEAD | awk '{i+=$1;d+=$2; printf "+%-6d -%-6d %s\n",$1,$2,$3} END {printf "TOTAL +%d -%d\n",i,d}'
git --no-pager diff --name-status HEAD~1 HEAD
```

Two numbers change the conversation before any splitting is discussed:

- **Production vs test insertions.** A 3,500-line diff where 1,000 lines are one new
  test file is not a 3,500-line review. Report the production-only figure; sometimes it
  dissolves the problem and no split is warranted.
- **Per-concern totals.** Group files by concern (below) and total each. If one group is
  85% of the diff and is atomic, splitting the remaining 15% is the entire available win
  - say so plainly rather than proposing a heroic three-way split.
- **Whether one file serves both concerns.** Two or three such files is normal and Step 4
  usually dissolves them; a dozen means the concerns are not actually separable and the
  honest answer is to leave the commit whole.

Use `--name-status` and not just `--numstat`: added (`A`) and deleted (`D`) files change
what the `git add` commands must look like, and a deletion left in the wrong PR breaks
the build in a way that is annoying to diagnose later.

## Step 1: Group by concern, not by directory

A concern is a decision someone made, and it usually shows up as: one layer's API
changing shape, one user-visible behaviour changing, or one file pair being deleted and
replaced. Directory is a weak proxy - a single `handlers/` directory routinely holds
files belonging to two different concerns.

For each candidate group, write one sentence naming what a reviewer would be asked to
approve. If you cannot write that sentence, it is not a concern, it is a pile of files.

## Step 2: Establish dependency direction - the only test that matters

A lower PR must build and pass **without** anything above it. That is a claim about
which symbols cross the cut, and it is cheap to check statically before touching git.

Find what the *pre-change* version of the upper half uses from the lower half's
packages, then confirm those symbols survive the lower half's changes:

```bash
# what the old upper-half code reaches for
for f in <upper-half files>; do
  echo "=== OLD $f ==="
  git show HEAD~1:$f | grep -oE "<pkg>\.[A-Za-z]+|<MethodName>|<TypeName>" | sort -u
done

# do those still exist after the lower half's changes?
grep -rn "func .*<Symbol>\|type <Symbol>" <lower half packages>
```

Two shortcuts that resolve most cases fast:

- **Unexported symbols cannot cross packages.** If the lower half renamed or deleted
  something lowercase, no other package could have referenced it - rule it out and move
  on. Only exported surface can break the upper half.
- **An unused exported method is harmless.** The lower PR may introduce an accessor that
  nothing calls until the upper PR arrives. That compiles fine and is often the cleanest
  way to keep the cut tidy (see Step 4).

If the direction runs the other way - the *lower* half needs something the upper half
introduces - the stack order is wrong. Reorder rather than forcing it.

## Step 3: Refuse to split what is atomic

Some refactors have no compiling intermediate, and saying so is the correct answer.

The signature: removing a field, method, or interface entry requires *simultaneously*
adding its replacement, flipping every reader, and updating every writer. Any partial
state either does not compile, or holds the same fact in two places at once - and
"limits live in two places for one commit" is harder to review, and easier to get wrong,
than the whole change at once.

Test it by asking: what would the intermediate commit look like, and would it build? If
the honest answer is "it would have both the old and new storage live simultaneously",
do not split. Recommend keeping it whole and explain why in one or two sentences.

Do not split a group purely to hit a line-count target. A mechanical rename can usually
be lifted out (it is skippable in review), but weigh the gain: twenty call sites moved
into their own PR is a modest win for an extra branch to manage.

## Step 4: Keep files whole - relocate small seams instead

The fiddliest case is one file whose diff serves both concerns, because splitting it
means staging individual hunks and every later rebase becomes manual.

Before accepting a hunk-level split, look for a small seam you can move **down-stack**
to make the file whole again. A seam is typically an interface entry plus its
implementation plus any mocks that satisfy the interface - a handful of lines. Moving it
into the lower PR (where it may sit unused) is almost always better than splitting a
test file across two branches.

Watch specifically for **mocks**: any test double implementing an interface must gain a
method the moment that interface does. That single fact often decides which PR an
interface entry belongs to.

## Step 5: Confirm the partition with the user before executing

Present the groups, the totals, which one is atomic and why, and the proposed order.
This is a judgment call about history shape - use `AskUserQuestion` if more than one
partition is defensible. In most repos branch and PR creation belongs to the user; check
whether you are expected to run the commands or hand them over.

## Step 6: Execute

Unstage the commit, keeping the working tree, then rebuild it as two commits:

```bash
git reset HEAD~1                      # mixed: HEAD and index move, tree keeps the new content

# --- lower PR: stage its paths explicitly ---
git add <explicit path list>          # directories are fine: plugins/foo plugins/bar
git commit -m "<type>(<scope>): <what this decision is>"

# --- upper PR: everything that is left ---
git add -A                            # picks up deletions and new files
gt create -m "<type>(<scope>): <the other decision>"

gt submit --stack
```

Stage the lower PR explicitly and let the upper PR be "everything remaining" (`git add
-A`). The reverse - explicitly staging the upper half - silently drops any file you
forgot into the wrong commit.

`git add -A` sweeps untracked files too, including ones that have nothing to do with
either half. Run `git status --short` and read the `??` lines before it: anything there
that is not part of the split needs staging by path instead, or the "everything
remaining" commit quietly acquires it.

The branch you started on becomes the lower PR. Rename it with `gt rename` if its name
now describes only part of what it holds.

## Step 7: Prove the lower PR stands alone

Do not skip this, and do not infer it from the static check in Step 2. Build the lower
half with the upper half genuinely absent.

**Check the repo's convention on `git stash` first.** Some repos forbid it outright,
because toolchain or lockfile churn makes pops conflict - look for a `CLAUDE.md` note or
a sibling skill that says so before reaching for it. Where it is forbidden, back the
upper half up and restore the tree instead, which is what the absorb workflow does:

```bash
# after the lower PR is committed (Step 6), for every upper-half file:
mkdir -p "<scratchpad>/$(dirname <file>)" && cp <file> "<scratchpad>/<file>"

# then put the tree back to the lower PR alone
git restore --source=HEAD --staged --worktree -- <upper-half paths>
```

`git restore` brings deleted files back and reverts edits, but will not touch a file git
has never seen - move intended *new* files aside by hand. Verify every backup exists
before restoring anything.

Then build and test:

```bash
for m in <modules>; do (cd $m && go build ./...) || echo "$m FAIL"; done
(cd <module> && go test -count=1 ./...)
```

Use `-count=1`. Go's test cache will happily report `ok` for a package whose result
predates the change you are verifying, which makes a broken split look clean - and will
do it while printing a plausible duration, so the output gives you no hint.

Restore the upper half from the scratchpad afterwards and confirm `git status --short`
lists exactly the files you expect before continuing.

## Graphite notes

- `gt split --by-hunk` is the built-in way to divide one commit, but it walks every hunk
  interactively. For a many-file split, rewriting the tip with plain git (Step 6) and
  then `gt create` is faster and easier to verify.
- `gt split --by-commit` is the right tool when the branch already contains several
  commits that each belong in their own PR - check `git log --oneline` first; if the
  commits already match the concerns, this is the whole job.
- Rewriting a tracked branch's tip with plain git is fine when it has no children. If it
  does, run `gt restack` afterwards and expect to resolve conflicts upstack.
- `gt log long` shows the resulting stack shape with messages - read it once before
  submitting to confirm the order and the messages match the decisions.
- To check for unresolved conflicts, ask git rather than grepping for markers:
  `git ls-files -u` is empty when nothing is unmerged. A regex like `^=======` also
  matches the `=====...` separator lines people put in comment banners, so it reports
  conflicts in files that were never touched.

## When to stop and ask

- Two partitions are both defensible, and they imply different review stories.
- The split would require hunk-level surgery on more than one file after Step 4.
- The lower PR fails Step 7 and the fix would mean moving a substantial amount of the
  upper PR down - that is a sign the cut is in the wrong place, not that it needs
  patching.
