- fix: scope cache keys per virtual key so a shared cache_key or default_cache_key never serves one virtual key's response to another; the vk: namespace is reserved and per-request threshold overrides are floored at the configured value and capped at 1.0 (#7862)
  <Warning>
  Entries written before the upgrade under a virtual key are not reused, so expect a cold cache. Unscoped cache keys starting with vk: are moved to raw:vk:. A per-request threshold can only raise the configured threshold.
  </Warning>
- chore: upgraded core to v1.11.2 and framework to v1.8.1
