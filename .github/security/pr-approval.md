# Admin approval before PR execution

All PR authors, including team members, external contributors, and bots, use the
same `pr-testing` environment gate. Only `@maximhq/bifrost-admin` may approve it.
The team has repository administrator access; its immutable team ID is 16787162.
Self-review and administrator bypass are disabled. No credentials belong in this
shared approval environment.

Every job in a PR workflow references the environment directly. This includes
setup, authorization, and skip-detection jobs: an approval job after an ungated
checkout is too late. Feature-branch Snyk and E2E pushes also wait, so a push
cannot bypass the PR gate. Manually selected test branches wait as well.
Named `dev`, `main`, and `v2.0.0` pushes and scheduled baseline tests retain their
existing automatic behavior. Merge-queue runs require approval.

Coverage in this change:

| Workflow | Jobs requiring approval for PR/feature testing |
| --- | --- |
| Workflow Lint | Offline policy checks |
| Manual network security validation | Explicitly requested network probe; generates expected alerts |
| Dependency Review | Dependency review |
| CodSpeed | Every benchmark matrix job |
| Snyk | Both source and code scans on feature branches |
| E2E Tests | UI test job on the feature branch |
| PR Tests | Skip detection and test execution |
| Run Core Tests | Authorization and test execution |
| Live Tests | Both manually dispatched test jobs |

## One person needs two accounts

GitHub never lets the account that pushed a commit approve it, and
`prevent_self_review` extends that to deployment approvals. With a single
account in `@maximhq/bifrost-admin`, every pull request and every release that
account pushes waits forever. A second human is not required: a second account
owned by the same administrator, used only to approve, satisfies every gate.
Keep it on a passkey sign-in in the browser with no `gh auth login`, no personal
access token and no OAuth grant, and add it to the team before merging these
workflow changes. The authoring account pushes; the approving account reviews
pull requests and approves deployments. A token stolen from the authoring
machine then has nothing to approve with. The live check does not inspect team
membership, so this is a manual precondition.

## Server-side configuration and verification

The environment name alone is not protection: GitHub otherwise creates an
unprotected environment. Apply the reviewed configuration before publishing the
workflow changes:

```sh
gh api --method PUT repos/maximhq/bifrost/environments/pr-testing \
  --input .github/security/pr-testing-environment.json
bash .github/workflows/scripts/run-security-tool.sh prapproval --live
bash .github/workflows/scripts/run-security-tool.sh prapproval --live --inventory
```

The PUT changes only environment protection; it does not approve, dispatch, or
rerun any workflow. The verification commands only read GitHub settings. Keep
the repository's fork approval setting at `all_external_contributors` as an
additional gate. That native gate permits users with write access to approve;
the environment adds the required admin-team review before jobs start.

Inspect the exact workflow diff and checked-out commit before approving. Approve
the current run through **Review deployments**. Do not use persistent approval
labels or exempt member/bot usernames. A new commit creates a separate run and
requires approval again. Reject unwanted runs instead of approving them to clear
checks. Existing runs and older stack branches do not acquire new YAML gates
retroactively.

## Limits that must remain visible

This is not a repository-wide GitHub scheduler policy. GitHub's native fork
approval setting cannot express "only repository admins may approve every
action." A contributor who can modify workflow YAML can remove an environment
reference. CODEOWNERS and a failing policy check prevent neither the initial
execution of that modified workflow nor a write-access maintainer approving it.
Enforcing that threat boundary requires a trusted workflow/runner controller
outside PR-controlled YAML, or narrowing who has repository write access.

GitHub-managed workflows are outside this repository's workflow files. The live
inventory currently includes CodeQL default setup, Copilot review/agent workflows,
Dependency Graph, Dependabot Updates, and Pages. It also retains old Lint and E2E
API workflow entries absent from `dev` and `main`. These are **not certified as
admin-gated** by this change. `--live --inventory` deliberately fails on every
unaccounted active workflow rather than silently treating it as covered.
Review each service's triggers and authorization; migrate automatic PR execution
to an admin-gated workflow or disable that automatic execution. Do not simply add
these workflows to an exception list. CodeQL default setup was still configured
when audited; no managed service was disabled as part of this change.

The `pr-testing` environment configuration was applied and read back on
2026-10-21. Workflow changes remain local until explicitly published and merged.
Consequently, the current repository is **not yet guaranteed to require admin
approval before every action on an external PR**.

GitHub references:

- [Fork approval permissions](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/approve-runs-from-forks)
- [Environment reviewers and bypass controls](https://docs.github.com/en/actions/reference/workflows-and-actions/deployments-and-environments)

## Validation

Run `bash .github/workflows/scripts/run-security-tool.sh test` and
`bash .github/workflows/scripts/run-security-tool.sh prapproval` locally. The regression
suite rejects ungated setup jobs, feature-push aliases, non-admin environments,
actor-based exemptions, and empty reviewer configurations. No live test workflow
is necessary for these checks. After an explicitly authorized rollout, verify a
member PR and a fork PR remain waiting with no runner steps started, then have a
different admin approve one exact run and confirm execution begins. This live
behavioral validation has not been performed.
