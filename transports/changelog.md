<!-- The pattern we follow here is to keep the changelog for the latest version -->
<!-- Old changelogs are automatically attached to the GitHub releases -->

- chore: version update core to 1.2.15 and framework to 1.1.17
- feat: add azure provider native responses API support
- chore: suppress irrelevant warnings in ListModels
- feat: refactored all plugin operations to completely async to prevent any blocking behavior
- feat: added provider level budget and rate limits using virtual keys
- feat: added streaming support in maxim plugin
- feat: improved retry logic for rate limiting errors