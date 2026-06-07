<Note>
  **Hotfix on [v1.5.9](https://docs.getbifrost.ai/changelogs/v1.5.9).** The key fix corrects wildcard (`*`) allowed-models handling: for providers whose models the catalog cannot enumerate, a wildcard now correctly allows any model instead of rejecting it. This affected keyless self-hosted providers (vLLM/Ollama/SGL) and custom providers without list-models support.
</Note>

## 🐞 Fixed

- **Virtual Key Usage Tracking Under User Attribution** - Usage is no longer silently dropped from virtual-key accounting whenever a user is attributed on a request. Governance now tracks both the virtual-key and user scopes by default; callers that deliberately want user-only accounting can opt in with the new `bifrost-skip-virtual-key-usage-tracking` context flag (#4123)
- **Wildcard Allow-Lists for Catalog-Opaque Providers** - A wildcard (`*`) allowed-models list now correctly permits any model for providers whose models the catalog cannot enumerate - custom providers without list-models support, and keyless self-hosted vLLM/Ollama/SGL - instead of incorrectly rejecting them (#4124)