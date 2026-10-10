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

Measure the whole branch against its base, not the tip against its parent: a branch that
already holds several commits is split as one diff, and `HEAD~1` would silently measure,
inspect and later reset only the last of them.

```bash
base="$(git merge-base "$(gt parent)" HEAD)"   # the branch's base, whatever the commit count
git --no-pager log --oneline "$base"..HEAD     # several commits that already match the concerns: see Graphite notes
git --no-pager diff --numstat "$base" HEAD | awk '{i+=$1;d+=$2; printf "+%-6d -%-6d %s\n",$1,$2,$3} END {printf "TOTAL +%d -%d\n",i,d}'
git --no-pager diff --name-status "$base" HEAD
```

Every later step reads `"$base"` from this shell, so run them in the same one or set it
again.

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

This procedure produces exactly two layers in one pass: a lower commit and an upper PR
that is everything else. A third concern is not a third output of this run - split it by
running this same procedure again on the resulting upper branch, treating it as the new
whole. "Groups" and "order" below describe how the *whole* diff's concerns relate to each
other and to picking which one goes into the lower layer first, not a promise that Step 6
and Step 8 produce more than two.

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
for f in "<upper-half file>" "<upper-half file>"; do
  echo "=== OLD $f ==="
  git show "$base:$f" | grep -oE "<pkg>\.[A-Za-z]+|<MethodName>|<TypeName>" | sort -u
done

# do those still exist after the lower half's changes?
grep -rn -- "func .*<Symbol>\|type <Symbol>" "<lower-half package dir>"
```

This is a heuristic, not a proof: it sees identifiers the regex happens to match and misses
fields, constants, methods, aliases and files the upper half adds. Step 7 is the proof.

Two shortcuts that resolve most cases fast:

- **Unexported symbols cross files, never packages.** A lowercase symbol the lower half
  renamed or deleted can only be referenced from files in the same package, so the check
  narrows to upper-half files that live in the lower half's own packages and rules the rest
  out. It does not vanish: a lower commit that drops an unexported helper a sibling file
  in the upper half still calls does not compile.
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

This confirmation is about the *shape* of the split, not the go-ahead to run it. Even
when only one partition is defensible, wait for the user's explicit go-ahead before
Step 6: everything from there on mutates the branch (`git reset`, `git commit`), and
Step 8 ends by publishing (`gt submit --stack`) - an outward-facing action that is
always the user's to run, obvious split or not.

## Step 6: Commit the lower PR

Start from a clean tree. A mixed reset keeps whatever is already modified or untracked,
and the staging below would fold it into one of the two PRs.

```bash
git status --porcelain=v1                    # must print nothing; if it does, sort the tree out first
git status --porcelain=v1 --ignored=matching  # must print nothing either
```

A plain `git status --porcelain=v1` never lists ignored files, and `git reset --hard`
in Step 7 does not touch them - so a stray ignored file already sitting in the tree
(a stale `.env`, a leftover build artifact) rides through undetected and is present
during Step 7's "lower half genuinely alone" validation for reasons that have nothing
to do with either half. Sort it out - move it aside or delete it - before continuing;
this procedure does not decide that for you.

```bash
git reset "$base"                     # mixed: HEAD and index move to the base, tree keeps the new content

# --- lower PR: stage its paths explicitly ---
git add -- "<path>" "<path>"          # one quoted argument per path
git commit -m "<type>(<scope>): <what this decision is>"
```

Stage the lower PR explicitly and leave the upper PR as "everything remaining". The
reverse - explicitly staging the upper half - silently drops any file you forgot into the
wrong commit. Quote every path and keep the `--`: a generated path list is data, and an
unquoted substitution splits on whitespace and honours shell metacharacters.

Name files, not directories, for any path Step 1 flagged as serving both concerns: `git
add` stages a directory recursively, and Step 1's own premise is that a directory
routinely holds files from both. Staging one such directory here would sweep upper-half
files into the lower commit right where the split was supposed to keep them apart. A
directory is fine to name only once you have confirmed every file under it belongs to
the lower half.

The upper half now sits uncommitted in the working tree. Do not commit it yet: Step 7 needs
it absent, and it is far easier to set aside while it is uncommitted than once it has
become a commit that `HEAD` points at.

## Step 7: Prove the lower PR stands alone

Do not skip this, and do not infer it from the static check in Step 2. Build the lower
half with the upper half genuinely absent - which is why it is still uncommitted: `HEAD`
is the lower commit, and everything else in the tree is the upper half.

Set the upper half aside as one patch rather than file by file. A patch carries added,
deleted, renamed and binary files uniformly, so there is no per-path `cp` to get wrong and
no deletion to reconstruct by hand. Some repos forbid `git stash` because toolchain or
lockfile churn makes pops conflict - look for a `CLAUDE.md` note or a sibling skill that
says so; the patch route avoids it either way.

```bash
lower="$(git rev-parse HEAD)"                                 # the lower PR's commit
git status --short                                            # read the ?? lines first (below)
git add -A                                                    # stage the whole upper half, new and deleted files included
git diff --cached --binary "$lower" > "<scratchpad>/upper.patch"
git apply --check --cached -R "<scratchpad>/upper.patch"      # the patch must reverse cleanly before anything is reset
git reset --hard "$lower"                                     # tree = lower PR alone
```

`git add -A` sweeps untracked files too, including ones that have nothing to do with either
half. Read the `??` lines before it: anything there that is not part of the split needs
moving aside first, or it becomes part of the upper PR.

Then build and test every module the split touches, and stop if any of them fails:

```bash
failed=0
for m in "<module dir>" "<module dir>"; do
  (cd -- "$m" && go build ./... && go vet ./... && go test -count=1 ./...) || { echo "$m FAIL" >&2; failed=1; }
done
[ "$failed" -eq 0 ]                                           # a lower PR that does not stand alone is not submitted
```

Use `-count=1`. Go's test cache will happily report `ok` for a package whose result
predates the change you are verifying, which makes a broken split look clean - and will
do it while printing a plausible duration, so the output gives you no hint.

`go build ./...` writes a binary into the working directory for every `main` package it
builds, and this repo's `.gitignore` does not name every one of them. Left in place, an
untracked binary from validating the lower half rides along into the upper PR the moment
Step 8 runs `git add -A`. Clean each module directory before bringing the upper half back:

```bash
for m in "<module dir>" "<module dir>"; do
  git clean -fd -- "$m"                                       # remove anything the build loop left behind
done
```

Bring the upper half back and confirm the tree is exactly what it was:

```bash
git apply --index "<scratchpad>/upper.patch"
git status --short                                            # the same upper-half paths as before the reset
```

A failure in this step means the cut is in the wrong place (see *When to stop and ask*),
not that the lower PR needs patching up.

## Step 8: Create the upper PR

Only once Step 7 passed and the upper half is back in the tree.

The branch you started on becomes the lower PR. If it already has an open PR, keep its
name: GitHub ties a PR to its branch name, so renaming strands the PR and a resubmit opens
a second one. Name only the new upper branch (`gt create` derives it from the message), and
reach for `gt rename` only when no PR exists yet and the name now describes part of what
the branch holds.

```bash
gh pr list --head "$(git branch --show-current)" --state open --json number,url   # non-empty: keep this name

git add -A                            # everything remaining is the upper PR
gt create -m "<type>(<scope>): <the other decision>"
```

`gt submit --stack` publishes both PRs. Stop here and confirm with the user before running
it - the confirmation in Step 5 covered the split's shape, not this. Never run it without
that go-ahead, whatever the split looked like.

```bash
gt submit --stack
```

## Graphite notes

- `gt split --by-hunk` is the built-in way to divide one commit, but it walks every hunk
  interactively. For a many-file split, rewriting the tip with plain git (Step 6) and
  then `gt create` is faster and easier to verify.
- `gt split --by-commit` is the right tool when the branch already contains several
  commits that each belong in their own PR - Step 0's `git log` shows whether it does; if
  the commits already match the concerns, this is the whole job.
- Rewriting a tracked branch's history with plain git (Step 6 resets to the base) is fine
  when it has no children. If it does, run `gt restack` afterwards and expect to resolve
  conflicts upstack.
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
