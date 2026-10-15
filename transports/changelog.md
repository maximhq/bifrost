## ✨ Features

- **Virtual Key Metadata in config.json** - `governance.virtual_keys[].metadata` accepts string key/value pairs (for example `cost_center`, `owner`) for cost attribution. Keys use letters, digits, `.`, `_` and `-`; values are up to 512 characters; up to 50 entries [@jimseiwert](https://github.com/jimseiwert)
- **Spend by Virtual Key Metadata** - Each log entry now carries a snapshot of its virtual key's metadata, so logs and dashboard spend can be filtered with the existing metadata filters (for example `metadata_cost_center=cc-42`). On a key collision the virtual key's value overrides `x-bf-lh-*` headers, configured logging headers and `x-bf-dim-*` dimensions, so callers cannot spoof attribution [@jimseiwert](https://github.com/jimseiwert)
- **Virtual Key Metadata API** - Create and update virtual keys with a `metadata` object (on update, omit to keep, `{}` to clear); invalid metadata returns a 400 naming the key. The virtual key list accepts `metadata_<key>=<value>` filters (AND-ed) and `search` also matches metadata [@jimseiwert](https://github.com/jimseiwert)
- **Virtual Key Metadata in the UI** - The virtual key sheet has a Metadata editor (validated like the API, editable on access-profile-managed keys too), the details sheet lists entries with a "View spend" link to the filtered logs, and the virtual keys table has a metadata filter [@jimseiwert](https://github.com/jimseiwert)

## 🗄️ Database Migrations

- `add_virtual_key_metadata_column` - adds the nullable `metadata` column to `governance_virtual_keys`. Existing keys keep NULL. Non-rollbackable: dropping the column would delete every key's metadata; older binaries ignore it.
