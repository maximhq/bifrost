# Gemini File Upload MIME Type

Minimal reproduction for Gemini Files uploaded through Bifrost's GenAI-compatible route.

## What It Reproduces

When a client starts a Gemini resumable upload through:

```http
POST /genai/upload/v1beta/files
X-Goog-Upload-Header-Content-Type: application/pdf
```

Bifrost converts that upload into a Gemini provider `FileUpload` request.

Before the fix, the MIME type from `X-Goog-Upload-Header-Content-Type` was not sent to Gemini file metadata or the multipart file part. Gemini accepted the upload, but the uploaded file could come back without a useful `mimeType`, and a later `generateContent` request with that file URI failed with a generic 400:

```text
Request contains an invalid argument.
```

After the fix, Bifrost sends the MIME type in both places:

```json
{
  "file": {
    "displayName": "tiny.pdf",
    "mimeType": "application/pdf"
  }
}
```

and the file multipart part has:

```http
Content-Type: application/pdf
```

## Run

From this folder:

```sh
BIFROST_URL="http://localhost:8080" \
BIFROST_API_KEY="..." \
uv run python repro.py
```

The script:

1. Starts a Gemini resumable upload through `POST /genai/upload/v1beta/files`.
2. Finalizes the upload with a tiny PDF containing `ok`.
3. Retrieves the uploaded file metadata and prints the observed `mimeType`.
4. Calls `POST /genai/v1beta/models/{model}:generateContent` with the uploaded file URI.
5. Deletes the uploaded file.

Optional:

```sh
MODEL="gemini-2.5-flash"
```

## Expected Result

Before the fix, the upload/retrieve metadata can show an empty or generic MIME type, and `generateContent` can return:

```text
400 Request contains an invalid argument.
```

After the fix, the metadata includes `mimeType=application/pdf`, and `generateContent` can read the PDF.
