- fix: record ttft in nanoseconds instead of milliseconds to avoid truncation to 0
- feat: add `routing_targets` table with 1:many relationship to `routing_rules`; migrates existing single-target rules to the new table with `weight=1`; drops legacy `provider` and `model` columns from `routing_rules`
- feat: add per-target `key_id` pinning support in `routing_targets`
[feat]: persist OpenRouter per-key provider routing config in config store and include it in key config hashes [@dannyball710](https://github.com/dannyball710)
