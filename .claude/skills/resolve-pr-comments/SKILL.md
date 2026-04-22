
---
name: resolve-pr-comments
description: Resolve all unresolved PR comments interactively. Makes local edits only—NEVER commits or pushes. Use when asked to resolve PR comments, address review feedback, handle CodeRabbit comments, or fix PR review issues. Invoked with /resolve-pr-comments <PR_NUMBER> or /resolve-pr-comments <owner/repo> <PR_NUMBER>.
allowed-tools: Read, Grep, Glob, Bash, Edit, Write, WebFetch, Task, AskUserQuestion, TodoWrite
---

# Resolve PR Comments

An interactive workflow to systematically address all unresolved PR review comments.

## Usage

```
/resolve-pr-comments <PR_NUMBER>
/resolve-pr-comments <owner/repo> <PR_NUMBER>
```

If no repo is specified, uses the current git repository's remote origin.

**Before starting the workflow** - if the flow is in Plan Model - ask if the user wants to move to default mode to solve the comments one by one. Mention that each PR resolve has planning attached to it.

## Workflow Overview

1. **Detect repository** - Get owner/repo from git remote or user input
2. **Fetch unresolved comments** - Use GitHub GraphQL API (REST doesn't expose resolved status). Paginate through `reviewThreads` (cursor-based) so all pages are checked when a PR has more than 100 threads.
3. **Create tracking file** - Maintain state across the session
4. **For each comment**:
   - Get full details and any existing replies
   - Show the diff view of existing code in a proper diff view
   - Before suggesting the fix - do the research via documentations. And present all that docs research and relevant links to the user with the fix. Use context 7. **MAKE SURE YOU DO THIS ALWAYS**
   - Present to user with options (FIX, REPLY, SKIP)
   - Wait for user decision
   - Execute the action
   - Update tracking
5. **Verify resolution** - Check remaining unresolved count
6. **Repeat until done** - Continue until all comments resolved

## Step 1: Detect Repository

If repository not provided, detect from git remote:

```bash
git remote get-url origin | sed -E 's|.*github.com[:/]([^/]+/[^/.]+)(\.git)?|\1|'
```

## Step 2: Fetch Unresolved Comments (GraphQL)

The REST API does NOT expose resolved/unresolved status. Use GraphQL.

**Important:** `reviewThreads` returns at most 100 threads per request. PRs with many review threads (e.g. large CodeRabbit reviews) need **pagination** or you will only see the first 100 threads and miss unresolved ones on later pages. Always paginate until `pageInfo.hasNextPage` is false so the count and list are complete.

### Single-page query (first 100 threads only)

```bash
gh api graphql -f query='
{
  repository(owner: "OWNER", name: "REPO") {
    pullRequest(number: PR_NUMBER) {
      reviewThreads(first: 100) {
        pageInfo { hasNextPage endCursor }
        nodes {
          isResolved
          comments(first: 1) {
            nodes {
              databaseId
              path
              body
              author { login }
            }
          }
        }
      }
    }
  }
}'
```

Extract unresolved (single page):
```bash
... | jq -r '.data.repository.pullRequest.reviewThreads.nodes[] | select(.isResolved == false) | "\(.comments.nodes[0].databaseId)|\(.comments.nodes[0].path)|\(.comments.nodes[0].author.login)"'
```

Avoid parsing `body` in the same jq pass if you paginate—comment bodies can contain control characters and break jq. Fetch full body per comment via REST when presenting (see Step 4).

### Paginate to collect all unresolved threads

Use cursor-based pagination so every thread is considered:

1. First request: `reviewThreads(first: 100)` (no `after`).
2. From the response: read `pageInfo.hasNextPage` and `pageInfo.endCursor`.
3. Next request: `reviewThreads(first: 100, after: $endCursor)`.
4. Append unresolved from this page to your list (id, path, author only).
5. Repeat from step 2 until `hasNextPage` is false.

Example loop (collects id|path|author for all unresolved; write to a file or variable):

```bash
CURSOR=""
while true; do
  if [ -z "$CURSOR" ]; then
    RESP=$(gh api graphql -f query='
      query {
        repository(owner: "OWNER", name: "REPO") {
          pullRequest(number: PR_NUMBER) {
            reviewThreads(first: 100) {
              pageInfo { hasNextPage endCursor }
              nodes {
                isResolved
                comments(first: 1) {
                  nodes { databaseId path author { login } }
                }
              }
            }
          }
        }
      }')
  else
    RESP=$(gh api graphql -f query='
      query($after: String) {
        repository(owner: "OWNER", name: "REPO") {
          pullRequest(number: PR_NUMBER) {
            reviewThreads(first: 100, after: $after) {
              pageInfo { hasNextPage endCursor }
              nodes {
                isResolved
                comments(first: 1) {
                  nodes { databaseId path author { login } }
                }
              }
            }
          }
        }
      }' -f after="$CURSOR")
  fi
  # Append unresolved from this page (id|path|author only; no body to avoid control chars)
  echo "$RESP" | jq -r '.data.repository.pullRequest.reviewThreads.nodes[] | select(.isResolved == false) | .comments.nodes[0] | "\(.databaseId)|\(.path)|\(.author.login)"'
  HAS_NEXT=$(echo "$RESP" | jq -r '.data.repository.pullRequest.reviewThreads.pageInfo.hasNextPage')
  CURSOR=$(echo "$RESP" | jq -r '.data.repository.pullRequest.reviewThreads.pageInfo.endCursor // ""')
  [ "$HAS_NEXT" != "true" ] || [ -z "$CURSOR" ] && break
done
```

Total unresolved count = number of lines output. Use the comment `databaseId` values to fetch full body via REST when presenting each comment (Step 4).

## Step 3: Create Tracking File

Create at `/tmp/pr-review/pr-<NUMBER>-comments.md`:

```markdown
# PR #<NUMBER> Comment Review (<owner>/<repo>)

## Summary
- Total unresolved: <count>
- Fixed: 0
- Replied: 0
- Skipped: 0

## Comments to Address
| # | ID | File | Issue | Status |
|---|-----|------|-------|--------|
| 1 | 12345 | src/foo.ts | Missing validation | pending |

## Actions Taken
| ID | Action | Details |
|----|--------|---------|
```

## Step 4: Present Each Comment

For each unresolved comment, present in this format:

```
**Comment #<N>/<TOTAL>: ID <ID> - <File>**

**What it says:**
<Summary of the comment's concern>

**Current code state:**
<Show relevant code snippet if applicable - READ THE FILE>

**Documentations referred**
For anything related to LLM calls (in /core module) - make sure you refer to the documentation. You have access to web_search and context7. and show that too 

**Options:**
1. **FIX** - <Describe what the fix would be>
2. **REPLY** - <Describe the reply explaining why no fix needed>
3. **SKIP** - Move on without action

**My recommendation:** <OPTION> - <Brief reasoning>

Go ahead?
```

### Getting Full Comment Details

```bash
gh api repos/OWNER/REPO/pulls/PR_NUMBER/comments --paginate | jq -r '.[] | select(.id == COMMENT_ID) | .body'
```

### Checking for Existing Replies

```bash
gh api repos/OWNER/REPO/pulls/PR_NUMBER/comments --paginate | jq '.[] | select(.id == COMMENT_ID or .in_reply_to_id == COMMENT_ID) | {id, user: .user.login, body: (.body | gsub("\n"; " ") | .[0:150])}'
```

## Step 5: Execute Actions

**CRITICAL: Do NOT reply to PR comments until changes are pushed to the remote.** The reviewer cannot verify fixes until the code is pushed. Collect all fixes locally. This skill NEVER commits or pushes—the user handles that manually.

### For FIX:
1. Make the code change using Edit tool
2. Before applying the changes take approval from the user. DO NOT DIRECTLY MAKE CHANGE BEFORE user says yes. Also give an option to suggest the changes to code.
3. Track the fix locally in the tracking file (do NOT reply yet)
4. Continue to next comment

### For REPLY (non-code responses like "out of scope", "intentional design"):
These can be posted immediately since they don't require code verification. Use the **replies** endpoint only (see below).

## Reply endpoint (use this only)

To reply to a review comment, use the dedicated replies endpoint. **Do not** use `POST .../pulls/PR_NUMBER/comments` with `in_reply_to` — that returns 422 (in_reply_to is not a permitted key for create review comment).

```bash
gh api repos/OWNER/REPO/pulls/PR_NUMBER/comments/COMMENT_ID/replies -X POST -f body="<your reply>"
```

- `COMMENT_ID` is the numeric comment id (same as GraphQL `databaseId` from the thread's first comment).
- Request body: only `body` (string). No `in_reply_to`, `commit_id`, or path params.

## Step 5b: Push and Reply to FIX comments

After ALL comments have been addressed locally:

1. Ask user if they have pushed these changes to remote. Yes/No
2. **Only after push succeeds**, reply to each FIX comment using the replies endpoint:
   ```bash
   gh api repos/OWNER/REPO/pulls/PR_NUMBER/comments/COMMENT_ID/replies -X POST -f body="Fixed - <description of change>. See updated code."
   ```

### Batch workflow (fix all → push → post all)

If the user says e.g. "resolve all comments then push then post", you may:
1. Apply all FIX and REPLY decisions locally (with user approval per comment or bulk approval).
2. Ask user to push.
3. After push, post all replies in sequence: FIX replies first, then any REPLY-only replies, using the same `.../comments/COMMENT_ID/replies` endpoint for each.

### Common Reply Templates

**Out of scope:**
```
This is a valid improvement but out of scope for this PR. Tracked for future work.
```

**Already addressed:**
```
Already addressed - <variable/file> now has <fix>. See line <N>.
```

**Intentional design:**
```
This is intentional. <Explanation of why the current approach is correct>.
```

**Different module:**
```
This comment refers to <module> which is a different module not modified in this PR. It's working as-is.
```

**Asking bot to verify:**
```
This is solved, can you check and resolve if done properly?
```

## Step 6: Verify Resolution

After addressing comments, check remaining unresolved count. If the PR has more than 100 review threads, use the same pagination loop as in Step 2 and count unresolved across all pages; a single-page query only sees the first 100 threads.

Single-page check (first 100 threads only):
```bash
gh api graphql -f query='...' | jq '[.data.repository.pullRequest.reviewThreads.nodes[] | select(.isResolved == false)] | length'
```

If count is 0 (across all pages), report success. If comments remain:
- Some bots (like CodeRabbit) take time to auto-resolve
- User may need to push code changes first
- Re-run the workflow to address remaining comments

## Important Notes

1. **NEVER commit or push changes** - This skill only makes local edits. The user handles `git add`, `git commit`, and `git push` themselves. Do not run any git commit or git push commands.
2. **NEVER reply "Fixed" until code is pushed** - The reviewer cannot verify fixes until they're on the remote. Make all fixes locally. Only reply to FIX comments after the user confirms they have pushed (the user pushes manually).
3. **Always read the file** before suggesting fixes - understand context
4. **Check for existing replies** in the thread before responding
5. **Wait for user approval** on each action - never auto-fix without confirmation
6. **Update tracking file** after each action
7. **Some bots are slow** - CodeRabbit may take minutes to auto-resolve after push
8. **User pushes manually** - This skill never commits or pushes; the user must push code changes before expecting auto-resolution of FIX actions

## Error Handling

- If `gh` not authenticated: `gh auth login`
- If repo not found: verify owner/repo spelling
- If PR not found: verify PR number exists
- If comment ID invalid: re-fetch unresolved comments (may have been resolved)
- If reply returns 422 "in_reply_to is not a permitted key": you are using the wrong endpoint. Use `POST .../pulls/PR_NUMBER/comments/COMMENT_ID/replies` with only `-f body="..."`, not the create-comment endpoint.