## ✨ Features

- **Virtual Key Metadata in config.json** - `governance.virtual_keys[].metadata` accepts string key/value pairs (for example `cost_center`, `owner`) for cost attribution. Keys use letters, digits, `.`, `_` and `-`; values are up to 512 characters; up to 50 entries [@jimseiwert](https://github.com/jimseiwert)

## 🗄️ Database Migrations

- `add_virtual_key_metadata_column` - adds the nullable `metadata` column to `governance_virtual_keys`. Existing keys keep NULL. Non-rollbackable: dropping the column would delete every key's metadata; older binaries ignore it.
