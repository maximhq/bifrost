# Go Test Server

A test MCP server written in Go that provides string manipulation, JSON validation, UUID generation, hashing, and encoding/decoding tools.

## Tools

### 1. string_transform
Performs string transformations.

**Parameters:**
- `input` (string, required): The input string to transform
- `operation` (string, required): Operation to perform - "uppercase", "lowercase", "reverse", "title"

**Example:**
```json
{
  "input": "hello world",
  "operation": "uppercase"
}
```

**Response:**
```json
{
  "input": "hello world",
  "operation": "uppercase",
  "result": "HELLO WORLD"
}
```

### 2. json_validate
Validates if a string is valid JSON.

**Parameters:**
- `json_string` (string, required): The JSON string to validate

**Example:**
```json
{
  "json_string": "{\"name\": \"test\"}"
}
```

**Response:**
```json
{
  "valid": true,
  "parsed": {"name": "test"}
}
```

### 3. uuid_generate
Generates a random UUID v4.

**Parameters:** None

**Response:**
```json
{
  "uuid": "550e8400-e29b-41d4-a716-446655440000"
}
```

### 4. hash
Computes hash of input string.

**Parameters:**
- `input` (string, required): The input string to hash
- `algorithm` (string, required): Hash algorithm - "md5", "sha256", "sha512"

**Example:**
```json
{
  "input": "hello",
  "algorithm": "sha256"
}
```

**Response:**
```json
{
  "input": "hello",
  "algorithm": "sha256",
  "hash": "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
}
```

### 5. encode
Encodes input string.

**Parameters:**
- `input` (string, required): The input string to encode
- `encoding` (string, required): Encoding type - "base64", "hex", "url"

**Example:**
```json
{
  "input": "hello world",
  "encoding": "base64"
}
```

**Response:**
```json
{
  "input": "hello world",
  "encoding": "base64",
  "encoded": "aGVsbG8gd29ybGQ="
}
```

### 6. decode
Decodes encoded string.

**Parameters:**
- `input` (string, required): The encoded input string to decode
- `encoding` (string, required): Encoding type - "base64", "hex", "url"

**Example:**
```json
{
  "input": "aGVsbG8gd29ybGQ=",
  "encoding": "base64"
}
```

**Response:**
```json
{
  "input": "aGVsbG8gd29ybGQ=",
  "encoding": "base64",
  "decoded": "hello world"
}
```

## Build and Run

```bash
# Build
go build -o bin/go-test-server

# Run
./bin/go-test-server
```

## Usage in Tests

```go
config := schemas.MCPClientConfig{
    ID:             "go-test-server",
    Name:           "GoTestServer",
    ConnectionType: schemas.MCPConnectionTypeSTDIO,
    StdioConfig: &schemas.MCPStdioConfig{
        Command: "/path/to/bin/go-test-server",
        Args:    []string{},
    },
}
```
