# Bifrost Logging System

Bifrost provides a flexible logging system that allows you to either use the built-in logger or implement your own custom logger.

## 1. Log Levels

Bifrost supports four log levels:

- `debug`: Detailed debugging information, typically only needed during development
- `info`: General informational messages about normal operation
- `warn`: Potentially harmful situations that don't prevent normal operation
- `error`: Serious problems that need attention and may prevent normal operation

## 2. Using the Default Logger

Bifrost comes with a built-in logger that writes to stdout/stderr with formatted timestamps and log levels. It's used by default if no custom logger is provided.

### Default Configuration

```golang
client, err := bifrost.Init(schemas.BifrostConfig{
    Account: &yourAccount,
    // Logger not specified, will use default logger with info level
})
```

### Customizing Default Logger Level

```golang
client, err := bifrost.Init(schemas.BifrostConfig{
    Account: &yourAccount,
    Logger:  bifrost.NewDefaultLogger(schemas.LogLevelDebug), // Set to debug level
})
```

### Default Logger Output Format

The default logger formats messages as:

```text
    [BIFROST-TIMESTAMP] LEVEL: message
    [BIFROST-TIMESTAMP] ERROR: (error: error_message)
```

Example outputs:

```text
    [BIFROST-2024-03-20T10:15:30Z] INFO: Initializing provider OpenAI
    [BIFROST-2024-03-20T10:15:31Z] ERROR: (error: failed to connect to provider)
```

## 3. Implementing a Custom Logger

You can implement your own logger by following the `Logger` interface:

```golang
type Logger interface {
    // Debug logs a debug-level message
    Debug(msg string)

    // Info logs an info-level message
    Info(msg string)

    // Warn logs a warning-level message
    Warn(msg string)

    // Error logs an error-level message
    Error(err error)
}
```

### Example Custom Logger Implementation

```golang
type CustomLogger struct {
    // Your logger fields
}

func (l *CustomLogger) Debug(msg string) {
    // Implement debug logging
}

func (l *CustomLogger) Info(msg string) {
    // Implement info logging
}

func (l *CustomLogger) Warn(msg string) {
    // Implement warning logging
}

func (l *CustomLogger) Error(err error) {
    // Implement error logging
}

// Using the custom logger
client, err := bifrost.Init(schemas.BifrostConfig{
    Account: &yourAccount,
    Logger:  &CustomLogger{},
})
```

## 4. Best Practices

1. **Log Level Selection**

   - Use `debug` for development and troubleshooting
   - Use `info` for production monitoring
   - Use `warn` for potential issues that don't affect functionality
   - Use `error` for critical issues that need immediate attention

2. **Custom Logger Implementation**

   - Ensure thread safety if your logger is used concurrently
   - Consider implementing log rotation for production environments
   - Include relevant context in log messages
   - Handle errors appropriately in your logging implementation

3. **Performance Considerations**
   - Avoid expensive operations in logging methods
   - Consider using async logging for high-throughput scenarios
   - Be mindful of log volume in production environments

Remember that logging is crucial for monitoring and debugging your Bifrost implementation. Choose the appropriate logging strategy based on your environment and requirements.
