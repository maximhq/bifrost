## ✨ Features

- **Virtual Key Metadata in config.json** - `governance.virtual_keys[].metadata` accepts string key/value pairs (for example `cost_center`, `owner`) for cost attribution. Keys use letters, digits, `.`, `_` and `-`; values are up to 512 characters; up to 50 entries [@jimseiwert](https://github.com/jimseiwert)
- **Virtual Key Metadata API** - Create and update virtual keys with a `metadata` object (on update, omit to keep, `{}` to clear); invalid metadata returns a 400 naming the key. The virtual key list accepts `metadata_<key>=<value>` filters (AND-ed) and `search` also matches metadata [@jimseiwert](https://github.com/jimseiwert)

## 🗄️ Database Migrations

- `add_virtual_key_metadata_column` - adds the nullable `metadata` column to `governance_virtual_keys`. Existing keys keep NULL. Non-rollbackable: dropping the column would delete every key's metadata; older binaries ignore it.
