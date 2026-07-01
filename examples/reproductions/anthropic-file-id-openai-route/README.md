# Anthropic File ID Through OpenAI-Compatible Chat

Minimal reproduction for Anthropic uploaded files sent through Bifrost's OpenAI-compatible chat route.

## What It Reproduces

When the routed model is Anthropic and an OpenAI-compatible chat request contains:

```json
{
  "type": "file",
  "file": {
    "file_id": "file_abc123",
    "filename": "tiny.pdf"
  }
}
```

Bifrost converts that block into an Anthropic `document` block.

Before the fix, the Anthropic request looked like:

```json
{
  "type": "document",
  "title": "tiny.pdf",
  "source": {
    "type": ""
  }
}
```

Anthropic rejects that because `source.type` is empty.

After the fix, it becomes:

```json
{
  "type": "document",
  "title": "tiny.pdf",
  "source": {
    "type": "file",
    "file_id": "file_abc123"
  }
}
```

## Run

From this folder:

```sh
BIFROST_URL="http://localhost:8080" \
BIFROST_API_KEY="..." \
uv run python repro.py
```

The script:

1. Uploads a tiny PDF to `POST /v1/files?provider=anthropic`.
2. Calls `POST /litellm/v1/chat/completions` with an Anthropic model and the uploaded `file_id`.
3. Prints the chat response status and body.
4. Deletes the uploaded file.

To skip upload and use an existing file:

```sh
BIFROST_URL="http://localhost:8080" \
BIFROST_API_KEY="..." \
FILE_ID="file_abc123" \
uv run python repro.py
```

Optional:

```sh
MODEL="anthropic/claude-sonnet-4-5-20250929"
```

## Expected Result

Before the fix, the chat call returns a 400 from Anthropic with an error like:

```text
document.source: Input tag '' found using 'type'
```

After the fix, the request reaches Anthropic with `source.type=file`.
