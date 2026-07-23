---
name: review-pr
description: Reviews a PR or diff with multi-angle finders and adversarial verification, then reports a findings table, a merge/no-merge recommendation, required followups, and offers to create a follow-up PR. Use when the user types /review-pr [PR# | branch | path].
---

# PR Review (bifrost custom)

## Scope
If a PR number is given: `gh pr view <N> --json title,body,author,baseRefName,headRefName,state,additions,deletions,changedFiles` and `gh pr diff <N>` define the scope. Otherwise use `git diff @{upstream}...HEAD` (fall back to `git diff dev...HEAD`, then `git diff HEAD` for uncommitted work).

The diff is the only review scope. When an angle needs surrounding code, Read files in this checkout if it matches the PR branch, otherwise fetch via `gh`.

## Process
1. **Find** - launch independent finder agents in parallel (Agent tool), each returning up to 6 candidates `{file, line, summary, failure_scenario}`:
   - Correctness angles: line-by-line diff scan (read the enclosing function of every hunk); removed-behavior audit (what invariant did deleted/replaced code enforce, and where is it re-established?); cross-file caller/callee trace (grep for changed symbols, check every call site).
   - Cleanup angles: reuse (does new code re-implement an existing helper? name it); simplification (redundant state, copy-paste variation, dead code); efficiency (wasted work, repeated I/O, hot-path cost); altitude (is the fix at the right layer, or a bandaid on shared infrastructure?); conventions (check AGENTS.md rules - only flag when you can quote the exact rule and the exact violating line).
   - Finders must pass through every candidate with a nameable failure scenario; do not self-censor half-believed ones.
2. **Verify** - dedup candidates that share a mechanism (keep the most concrete scenario), then run one verifier agent per candidate. Each returns exactly one of CONFIRMED (quote the triggering inputs and the wrong output), PLAUSIBLE (mechanism real, trigger uncertain - state what would confirm it), or REFUTED (quote the line that proves it wrong). Drop REFUTED.

## Output format (required)
1. **2-3 sentence overview** of what the PR does and its overall quality.
2. **Findings table**: columns `# | Severity | Location | Finding | Verdict`, ranked most-severe first, max 8 rows. Location is a clickable `file:line`. If nothing survived verification, say so plainly.
3. **Merge recommendation**: exactly one of **Merge**, **Merge after nits**, or **Not yet - request changes**, with a 1-2 sentence reason tied to specific finding numbers. Only CONFIRMED correctness findings may block a merge; PLAUSIBLE and cleanup findings are nits or followups.
4. **Followups required**: numbered list, each tagged "in this PR (blocking)" or "follow-up PR", with concrete file:line targets and a one-line fix description.
5. **Follow-up PR offer**: if any follow-up items exist, offer to prepare them on a new branch. NEVER commit or push - leave changes staged for the user (standing user rule), unless the user explicitly authorizes committing and opening the PR with `gh pr create`.
6. Briefly list refuted candidates with the one-line reason, so the user knows what was checked and cleared.

## Post the review to GitHub (after user approval)

When reviewing a GitHub PR, after presenting the output above, offer to publish it on the PR. Only post after the user approves (posting is outward-facing). When approved:

1. Build a single review payload as JSON in the scratchpad and submit it with `gh api repos/{owner}/{repo}/pulls/<N>/reviews --input <file>.json` (get owner/repo via `gh repo view --json nameWithOwner`).
2. `event`: use `REQUEST_CHANGES` when the merge recommendation is "Not yet", `COMMENT` for "Merge after nits", `APPROVE` for "Merge".
3. `body`: the overview, findings table, merge recommendation, suggested followups (tagged blocking vs follow-up PR), and the refuted-candidates list.
4. `comments`: one inline comment per finding whose file is part of the PR diff, anchored with `path`, `line` (new-file line number, computed from the diff hunk headers), and `side: "RIGHT"`. Include the concrete failure scenario and a suggested fix as a code block. Findings in files NOT touched by the diff cannot be inline - keep them in the review body (GitHub rejects comments on unchanged files).
5. Report the resulting review URL back to the user.

## House rules
- Never dismiss a finding as "out of scope" - address it or give a concrete design reason.
- No em dashes anywhere in the output.
- Diagrams, if any, are mermaid blocks, never ASCII art.
- Capability claims about providers must cite fetched upstream docs.
