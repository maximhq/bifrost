[feat]: add historical log payload offload backfill [@qixiangyang](https://github.com/qixiangyang)
- fix: replace streaming gate replay-buffer size accounting with cached zero-marshal estimates (eliminates per-chunk MarshalJSON on the full-hold path)
