# Maxim-SDK Plugin for Bifrost

This plugin integrates the Maxim SDK into Bifrost, enabling seamless observability and evaluation of LLM interactions. It captures and forwards inputs/outputs from Bifrost to the Maxim's observability platform. This facilitates end-to-end tracing, evaluation, and monitoring of your LLM-based application.

## Usage for Bifrost Go Package

1. Download the Plugin

   ```bash
   go get github.com/maximhq/bifrost/plugins/maxim
   ```

2. Initialise the Plugin

   ```go
       maximPlugin, err := maxim.NewMaximLoggerPlugin("your_maxim_api_key", "your_maxim_log_repo_id")
       if err != nil {
           return nil, err
           }
   ```

3. Pass the plugin to Bifrost

```go
    client, initErr := bifrost.Init(schemas.BifrostConfig{
        Account: &yourAccount,
        Plugins: []schemas.Plugin{maximPlugin},
        })
```

## Usage for Bifrost HTTP Transport

1. Set up the environment variables

   ```bash
   export MAXIM_API_KEY=your_maxim_api_key
   export MAXIM_LOG_REPO_ID=your_maxim_log_repo_id
   ```

2. Set up flags to add the plugin
   Add `maxim` to the `--plugins` flag

   e.g., `npx -y @maximhq/bifrost -plugins maxim`

   For docker build

   ```bash
   docker build -t bifrost-transports .
   ```

   Running the docker container

   > **💡 Volume Mounting**: The entire working directory is mounted to `/app/data` to persist both the JSON configuration file and the database. This ensures that configuration changes made via the web UI are preserved between container restarts, and the new hash-based configuration loading system can properly track file changes.

   ```bash
   docker run -d \
    -p 8080:8080 \
    -v $(pwd):/app/data \
    -e APP_PORT=8080 \
    -e MAXIM_API_KEY \
    -e MAXIM_LOG_REPO_ID \
    bifrost-transport
   ```

## Viewing Your Traces

1. Log in to your [Maxim Dashboard](https://getmaxim.ai/dashboard)
2. Navigate to your repository
3. View detailed llm traces, including:
   - LLM inputs/outputs
   - Tool usage patterns
   - Performance metrics
   - Cost analytics

## Additional Features

The plugin also supports custom `session-id`, `trace-id` and `generation-id` if the user wishes to log the generations to their custom logging implementation. To use it, pass your trace ID to the request context with the key `trace-id`, and similarly `generation-id` for generation ID. In these cases, no new trace/generation is created and the output is logged to your provided generation. Likewise, `session-id` can be used to add the traces to your generated session.

e.g.

```go
    ctx = context.WithValue(ctx, "generation-id", "123")

    result, err := bifrostClient.ChatCompletionRequest(schemas.OpenAI, &schemas.BifrostRequest{
        Model: "gpt-4o",
        Input: schemas.RequestInput{
            ChatCompletionInput: &messages,
            },
            Params: &params,
            }, ctx)
```

HTTP transport offers out-of-the-box support for this feature (when the Maxim plugin is used). Pass `x-bf-maxim-session-id`, `x-bf-maxim-trace-id`, or `x-bf-maxim-generation-id` headers with your request to use this feature.

## Testing Maxim Logger

To test the Maxim Logger plugin, you'll need to set up the following environment variables:

```bash
# Required environment variables
export MAXIM_API_KEY=your_maxim_api_key
export MAXIM_LOGGER_ID=your_maxim_log_repo_id
export OPENAI_API_KEY=your_openai_api_key
```

Then you can run the tests using:

```bash
go test -run TestMaximLoggerPlugin
```

The test suite includes:

- Plugin initialization tests
- Integration tests with Bifrost
- Error handling for missing environment variables

Note: The tests make actual API calls to both Maxim and OpenAI, so ensure you have valid API keys and sufficient quota before running the tests.

After the test is complete, you can check your traces on [Maxim's Dashboard](https://www.getmaxim.ai)
