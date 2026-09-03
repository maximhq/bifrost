## ✨ Features

- **Virtual Key Rotation Cooldown** - New `client.vk_rotation_cooldown` setting (duration string, e.g. "5m"): after a rotation the previous key value keeps authenticating until the grace window expires. config.json VK sync now treats a changed value as an explicit rotation (with console warning) and recognizes the previously rotated-out value as "no change".
- **Agent Capability Router** - Registers the native `agent-capability-router` plugin so configured dynamic agent aliases can be classified into deterministic role/capability lanes before CEL routing and model catalog resolution.
