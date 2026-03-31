
---
name: resolve-pr-comments
description: Resolve all unresolved PR comments interactively. Use when asked to resolve PR comments, address review feedback, handle CodeRabbit comments, or fix PR review issues. Invoked with /resolve-pr-comments <PR_NUMBER> or /resolve-pr-comments <owner/repo> <PR_NUMBER>.
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
2. **Fetch unresolved comments** - Use GitHub GraphQL API (REST doesn't expose resolved status)
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

The REST API does NOT expose resolved/unresolved status. Use GraphQL:

```bash
gh api graphql -f query='
{
  repository(owner: "OWNER", name: "REPO") {
    pullRequest(number: PR_NUMBER) {
      reviewThreads(first: 100) {
        nodes {
          isResolved
          comments(first: 1) {
            nodes {
              databaseId
              path
              body
            }
          }
        }
      }
    }
  }
}'
```

Extract unresolved comments:
```bash
... | jq -r '.data.repository.pullRequest.reviewThreads.nodes[] | select(.isResolved == false) | "\(.comments.nodes[0].databaseId)|\(.comments.nodes[0].path)|\(.comments.nodes[0].body | gsub("\n"; " ") | .[0:100])"'
```

Count unresolved:
```bash
... | jq '[.data.repository.pullRequest.reviewThreads.nodes[] | select(.isResolved == false)] | length'
```

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

**CRITICAL: Do NOT reply to PR comments until changes are pushed to the remote.** The reviewer cannot verify fixes until the code is pushed. Collect all fixes locally first, then push, then reply.

### For FIX:
1. Make the code change using Edit tool
2. Before applying the changes take approval from the user. DO NOT DIRECTLY MAKE CHANGE BEFORE user says yes. Also give an option to suggest the changes to code.
3. Track the fix locally in the tracking file (do NOT reply yet)
4. Continue to next comment

### For REPLY (non-code responses like "out of scope", "intentional design"):
These can be posted immediately since they don't require code verification:
```bash
gh api repos/OWNER/REPO/pulls/PR_NUMBER/comments/COMMENT_ID/replies -X POST -f body="<your reply>"
```

## Step 5b: Push and Reply to FIX comments

After ALL comments have been addressed locally:

1. Ask user to if they have pushed these changes to remote. Yes/No
2. **Only after push succeeds**, reply to FIX comments:
   ```bash
   gh api repos/OWNER/REPO/pulls/PR_NUMBER/comments/COMMENT_ID/replies -X POST -f body="Fixed - <description of change>. See updated code."
   ```

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

After addressing comments, check remaining count:

```bash
gh api graphql -f query='...' | jq '[.data.repository.pullRequest.reviewThreads.nodes[] | select(.isResolved == false)] | length'
```

If count is 0, report success. If comments remain:
- Some bots (like CodeRabbit) take time to auto-resolve
- User may need to push code changes first
- Re-run the workflow to address remaining comments

## Important Notes

1. **NEVER reply "Fixed" until code is pushed** - The reviewer cannot verify fixes until they're on the remote. Make all fixes locally, push, THEN reply.
2. **Always read the file** before suggesting fixes - understand context
3. **Check for existing replies** in the thread before responding
4. **Wait for user approval** on each action - never auto-fix without confirmation
5. **Update tracking file** after each action
6. **Some bots are slow** - CodeRabbit may take minutes to auto-resolve after push
7. **Push code changes** before expecting auto-resolution of FIX actions

## Error Handling

- If `gh` not authenticated: `gh auth login`
- If repo not found: verify owner/repo spelling
- If PR not found: verify PR number exists
- If comment ID invalid: re-fetch unresolved comments (may have been resolved)