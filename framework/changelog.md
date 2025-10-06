<!-- The pattern we follow here is to keep the changelog for the latest version -->
<!-- Old changelogs are automatically attached to the GitHub releases -->

- Chore: Adds ctx to each function to gracefully shutdown ongoing tasks and bring better concurrency management
- Fix: Fixes pricing sync to make sure latest updates are synced at every restart.
- Feat: Adds new accumulator for accumulating all streaming responses from LLMs.