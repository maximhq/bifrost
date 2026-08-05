- feat: add `matview_refresh_interval: "manual"` to keep the logstore materialized views (created, repaired, and refreshed once at boot) while moving the recurring refresh out of the serving process; distinct from `"off"`, which disables view maintenance entirely
[feat]: add process-wide skip-startup-migrations switch (`migrator.SetSkipStartupMigrations`) honored by configstore, logstore, and ClickHouse migration triggers; SQL stores fail fast when migrations are pending and the switch is set
- feat: add proxy support for WebSocket-based realtime calls, mirroring existing HTTP proxy configuration (#5788)
- perf: bulk virtual key provider replacement and direct VK lookup to eliminate per-provider round trips in the config store (#5844)
