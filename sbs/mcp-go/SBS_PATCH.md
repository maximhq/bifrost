# SBS fork: vendored github.com/maximhq/mcp-go v0.0.0-20261102181017-b7da3bc145a1
# (the mcp-go replacement upstream Bifrost uses), plus ToolAnnotation.Extra / Marshal/UnmarshalJSON
# in mcp/tools.go so custom tool annotations survive. Wired in by sbs/build.sh via go.work replace.
- client/client.go: SetGlobalNotificationHook, so core/mcp can route notifications/progress by token
  (reset-on-progress tool timeouts) without a handler per connection.
