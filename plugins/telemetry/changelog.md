- chore: upgraded Go to 1.27.3, core to v1.13.1 and framework to v1.10.1

## 🐞 Fixed

- **Error Requests Counter** - `bifrost_error_requests_total` now records streaming requests that fail before the stream starts (a governance or plugin rejection: 400, 401, 403, 429 or a pre-dispatch 5xx), which were dropped as intermediate chunks. The caller always saw the error and the non-stream form of the same failure was already counted, so stream error rates built on this counter were under-reported; they will now rise to the correct value. An error delivered inside a committed stream, where the caller already received a 200, is unchanged
