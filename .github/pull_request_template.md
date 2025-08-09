## Summary

Briefly explain the purpose of this PR and the problem it solves.

## Changes

- What was changed and why
- Any notable design decisions or trade-offs

## Type of change

- [ ] Bug fix
- [ ] Feature
- [ ] Refactor
- [ ] Documentation
- [ ] Chore/CI

## Affected areas

- [ ] Core (Go)
- [ ] Transports (HTTP)
- [ ] Providers/Integrations
- [ ] Plugins
- [ ] UI (Next.js)
- [ ] Docs

## How to test

Describe the steps to validate this change. Include commands and expected outcomes.

```sh
# Core/Transports
go version
go test ./...

# UI
cd ui
pnpm i || npm i
pnpm test || npm test
pnpm build || npm run build
```

If adding new configs or environment variables, document them here.

## Screenshots/Recordings

If UI changes, add before/after screenshots or short clips.

## Breaking changes

- [ ] Yes
- [ ] No

If yes, describe impact and migration instructions.

## Related issues

Link related issues and discussions. Example: Closes #123

## Security considerations

Note any security implications (auth, secrets, PII, sandboxing, etc.).

## Checklist

- [ ] I read `docs/contributing/README.md` and followed the guidelines
- [ ] I added/updated tests where appropriate
- [ ] I updated documentation where needed
- [ ] I verified builds succeed (Go and UI)
- [ ] I verified the CI pipeline passes locally if applicable


