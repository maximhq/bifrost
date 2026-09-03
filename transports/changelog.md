## ✨ Features

- **Virtual Key Rotation Cooldown** - New `client.vk_rotation_cooldown` setting (duration string, e.g. "5m"): after a rotation the previous key value keeps authenticating until the grace window expires. config.json VK sync now treats a changed value as an explicit rotation (with console warning) and recognizes the previously rotated-out value as "no change".
- feat: parse `x-bf-stream-idle-timeout` header for a per-request stream idle timeout override [@public-aanp-tanium](https://github.com/public-aanp-tanium)
- feat: parse the `x-bf-request-timeout` header (duration string or seconds integer) into `BifrostContextKeyRequestTimeout`, clamped to 30m, mirroring the existing `x-bf-session-ttl` / `x-bf-stream-idle-timeout` (#4805) parsing shape [@public-aanp-tanium](https://github.com/public-aanp-tanium)

## 🐞 Fixed

- **Bedrock Null Content on Empty Assistant Messages** - An assistant message with no text and no tool calls no longer serializes as `content:null`, which Converse rejected outright (#2765)
- fix: clamp the seconds form of `x-bf-request-timeout` and `x-bf-stream-idle-timeout` before converting to `time.Duration`, so a seconds value large enough to overflow int64 can no longer wrap into a small duration that bypasses the 30m ceiling [@public-aanp-tanium](https://github.com/public-aanp-tanium)
