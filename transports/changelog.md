## ✨ Features

- **Virtual Key Rotation Cooldown** - New `client.vk_rotation_cooldown` setting (duration string, e.g. "5m"): after a rotation the previous key value keeps authenticating until the grace window expires. config.json VK sync now treats a changed value as an explicit rotation (with console warning) and recognizes the previously rotated-out value as "no change".
- feat: add a Prompt Caching tab to provider settings for auto-injected cache breakpoints, with a TTL selector and an editor for role/index injection points, plus an `x-bf-prompt-cache-auto-inject` header to override the setting for a single request
