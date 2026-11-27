# CI and release automation

Use shell or Go for new CI, security, and release automation and their tests.
Do not introduce Python scripts, embedded Python, or Python dependency installers
for these tasks. This is an explicit maintainer preference. Existing application
SDK compatibility tests are separate from this automation policy.

Every Go function needs a godoc. Go filenames use lowercase concatenated words;
the `_test.go` suffix is the only underscore exception.

Changes to CI approval must cover external contributors, team members, bots,
feature-branch push aliases, and preparatory jobs. Approval must come from the
repository admin team before a runner starts. Never treat an environment name
without verified server-side required reviewers as an approval control.
