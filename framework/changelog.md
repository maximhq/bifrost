- feat: batch accounting: the `batch_jobs` table and its lifecycle store API (`UpsertBatchJob`, `GetBatchJob`, `ListDueBatchJobs`, `ClaimBatchJob`, `MarkBatchJobAggregateLogWritten`, `MarkBatchJobGovernanceReported`, `CompleteBatchJob`, `MarkBatchJobUnpriceable`, `FailBatchJob`) with runner fencing on `claimed_at` and `user_id`, `team_id`, `customer_id` and `source_log_id` attribution so settlement carries the creating request's identity; `batch_debug` on logs; batch pricing in the model catalog (`computeBatchTextCost` with catalog batch rates and a 0.5 default ratio, `CalculateBatchCostDetailsForUsage`, `BatchResultsRequest` routed through the batch path); and `persistRecalcOutcomes` shared by foreground and background recalculation (thanks [@SahilChoudhary22](https://github.com/SahilChoudhary22)!) (#5292, #5293, #6505)
- feat: input/output/additional cost split: denormalized `input_cost`, `output_cost` and `additional_cost` columns on logs, carried through matviews, ClickHouse, the hybrid store, cost recalculation and the quota API, populated on fallback billing paths, with semantic cache cost folded into additional cost
- feat: Bifrost overhead latency: `upstream_latency` and `overhead_latency` columns on logs with avg, p90, p95 and p99 overhead aggregates in `mv_logs_hourly`, ClickHouse, Postgres `percentile_cont` and the Go-side SQLite/MySQL histograms, the `overhead_breakdown` column for the per-span self-time decomposition, and `CompleteAndFlushTrace` handing connectors a copy of the trace without breakdown spans unless the plugin implements `OverheadSpanConsumer` (#5533, #5534, #6388, #6389)
- feat: `video_edit_input` column on logs for the new video edit request type (#6270)
- feat: new pricing columns and cost computation: megapixel-tier image fields (`output_cost_per_image_above_{4,8,16,32,64}_megapixels`) with a unified pixel-count tier ladder in `computeImageOutputCost`; per-size and joint size+quality image rates for 1024x1536 and 1536x1024 with a priority chain of size+quality, quality-only, size-only, then flat per-image rate, and `parseImageDimensions` so portrait and landscape sizes with equal pixel counts price correctly; `input_cost_per_query` for rerank; and `ultrafast` service tier rates (#6082, #6379, #6396)
- feat: notifications store: `TableNotification`, `NotificationStore`, `CreateNotification` and `ListNotifications` with JSON-serialized role IDs (#6207)
- feat: `gencache` generation-stamped memo cache, with `GetProvidersForModel` and `GetModelsForProvider` memoized until any backing store advances its write generation (#5641, #6224)
- feat: `DimensionScope` in `queryscope` and `applyDimensionCeiling` on rankings, histograms and key-pair queries so grouped analytics only expose organisation ids the caller may see; `getAvailableFilterData` no longer passes an empty id list to the redaction lookups, which returned every row (#6262)
- feat: `ObservabilityLimits` (per-plugin semaphore size and inject timeout) with context-bounded `Inject` calls and `DeadlineExceeded` accounting (#6341)
- feat: `GetSharedOauthTokensByConfigIDs` batch lookup on the config store so shared-OAuth MCP clients project `needs_reauth` when their token row is invalidated (#6429)
- feat: `mcp_library_sync_interval: 0` disables MCP library sync (`MCPLibrarySyncDisabled`), `file://` catalog URLs resolve through `datasheet.FilePathFromURL` without retry backoff, and `ResolveFrameworkPricingConfig` no longer backfills a zero interval (#6195)
- feat: `ReloadComplexityAnalyzerConfig` on `ServerCallbacks` for the routing handler (#6146)
- feat: `service_tier` copied from the processed stream response into `StreamAccumulatorResult` in `ProcessStreamingChunk` (#6236)
- fix: resolve runtime provider `together` (and variants such as `together_ai`, matched with `strings.Contains`) to the datasheet identity for catalog reads and price configured aliases through `AliasConfig.ModelName`, then `ModelID`, then the alias key (thanks [@dani29](https://github.com/dani29)!) (#6257, #6320)
- fix: escape every RediSearch special character in TAG query values in the Redis vector store, iterating bytes rather than runes (thanks [@AdityaPainuli](https://github.com/AdityaPainuli)!) (#5351)
- fix: redact sensitive and identity-aware-proxy request headers at `SetTraceRequestHeaders` so every connector (Datadog, OTEL, BigQuery, Kafka, Pub/Sub) receives redacted values (#6371)
- fix: `supports_none_reasoning_effort` datasheet flag wired through `extractSupportedParams` and `dropUnsupportedParams` so models that reason by default get `reasoning.effort: "none"` instead of losing `reasoning` (#6293)
- fix: reject negative `tool_sync_interval` at `UpdateMCPClientConfig` and on config file load, treat the value as whole minutes, and carry the stored global interval into `GetMCPConfig` (#6409, #6502)
- perf: `spanHandle` carries the `*Span` pointer so `EndSpan`, `SetAttribute` and `SpanFromHandle` skip the per-call trace and span scan, alongside bulk span attribute writes and reusable delivery timers in the tracing hot path (#5657, #5956, #6387)
- chore: remove legacy `gen_ai.*` attribute emission from the tracer in favor of the canonical `bifrost.*` keys (#6403)
- chore: close leaked Postgres pools and a stale hardcoded date in logstore tests (#6351)
- chore: build with Go 1.26.6 (#6269)
- chore: upgraded core to v1.8.0

<Warning>
This release adds 13 database migrations (7 configstore, 6 logstore). All are additive and reversible: each rollback drops the column, table or index it created, and `logs_recreate_matviews_with_cost_breakdown` is a no-op both ways because `repairMatViewShapes` rebuilds `mv_logs_hourly` on the next startup.
</Warning>

<Warning>
**High-throughput deployments: run the logstore migrations during a low-activity window.**

Five of the six logstore migrations alter `logs`, the highest-insert table in Bifrost, and the hourly matview is rebuilt against the full table on the first boot after upgrading. Schedule the upgrade for a low-traffic period, or expect elevated log-write latency while the migrations run.
</Warning>
