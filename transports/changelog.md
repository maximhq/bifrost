## 🐞 Fixed

- **Inference Auth via Virtual Key** — Inference authentication is now delegated entirely to the governance plugin (the authoritative virtual-key validator). Virtual-key-authenticated inference requests no longer return `401 Unauthorized` when dashboard password auth is enabled, and admin-password auth is now exclusive to dashboard/API routes — it is never required for inference.
