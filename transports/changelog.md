## 🐞 Fixed

- **Admin Password Redaction-Mask Collisions** - Valid admin passwords matching the generic 32-character secret mask are validated and hashed as new credentials instead of preserving the stored password (thanks [@G-XD](https://github.com/G-XD)!) (#6414)
- **Disabled Auth Password Validation and Hashing** - New admin passwords, including resolved secret references, are validated and hashed when submitted while disabling auth (thanks [@G-XD](https://github.com/G-XD)!) (#6414)
- **Partial MCP Sync Interval Updates** - `/api/config` updates preserve the stored global interval when the field is omitted, while an explicit `0` restores the built-in default.
