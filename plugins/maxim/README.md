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

3.  Pass to plugin to bifrost

```go
    client, err := bifrost.Init(schemas.BifrostConfig{
        Account: &yourAccount,
        Plugins: []schemas.Plugin{maximPlugin},
        })
```

## Usage for Bifrost HTTP Transport

1. Set up the environment variables

```bash
export MAXIM_API_KEY=your_maxim_api_key
```

2. Setup flags to use the plugin
   Use include `maxim` in `--plugins` and your maxim log repo id in `--maxim-log-repo-id"`

   eg. `bifrost-http -config config.json -env .env -plugins maxim -maxim-log-repo-id your_maxim_log_repo_id`

   For docker build

   ```bash
   docker build \
   --build-arg CONFIG_PATH=./config.example.json \
   --build-arg ENV_PATH=./.env.sample \
   --build-arg PORT=8080 \
   --build-arg POOL_SIZE=300 \
   --build-arg PLUGINS=maxim \
   --build-arg MAXIM_LOG_REPO_ID=your_maxim_log_repo_id \
   -t bifrost-transports .
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

The plugin also supports custom `trace-id` and `generation-id` if the uses wish to log the generations to their custom logging implementation. To use it, just pass your trace id to the passed request context with the key `trace-id`, and similarly to `generation-id` for generation id. In these cases no new trace/generation is created and the output is just logged to your provided generation.

eg.

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

HTTP transport offers out of the box support for this feature(when maxim plugin is used), just pass `x-bf-maxim-trace-id` of `x-bf-maxim-generation-id` header with your request to use this feature.
