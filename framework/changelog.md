## ✨ Features

- **Virtual Key Rotation Grace Period** - New rotation-state columns on `governance_virtual_keys` (`previous_value`, `previous_value_hash`, `previous_value_expires_at`, `rotated_at`) with encryption support, plus a `vk_rotation_cooldown` client config setting (duration string, default 0 = immediate flip) controlling how long a rotated-out key value keeps authenticating.
