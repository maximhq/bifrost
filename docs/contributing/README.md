# 🤝 Contributing to Bifrost

Welcome to the Bifrost community! We're excited to have you contribute to making AI model integration easier for everyone.

## 📑 Table of Contents

- [🚀 Getting Started](#-getting-started)
- [💻 Development](#-development)
- [🧪 Testing](#-testing)
- [📝 Documentation](#-documentation)
- [🎨 Code Style](#-code-style)
- [🔄 Pull Request Process](#-pull-request-process)
- [🐛 Bug Reports](#-bug-reports)
- [💡 Feature Requests](#-feature-requests)

---

## 🚀 Getting Started

### Ways to Contribute

| Contribution Type                                         | Skill Level  | Time Commitment |
| --------------------------------------------------------- | ------------ | --------------- |
| **[🐛 Bug Reports](bug-reports.md)**                      | Any          | 5-15 minutes    |
| **[📝 Documentation](documentation.md)**                  | Any          | 15-60 minutes   |
| **[🧪 Testing](testing.md)**                              | Intermediate | 30-120 minutes  |
| **[💻 Development](development.md)**                      | Advanced     | 2-8 hours       |
| **[🔌 Integrations](../features/integrations/README.md)** | Advanced     | 4-16 hours      |

### Quick Start

1. **Fork the repository**

   ```bash
   git clone https://github.com/YOUR_USERNAME/bifrost.git
   cd bifrost
   ```

2. **Set up development environment**

   ```bash
   # Install Go 1.23+
   go version

   # Install dependencies
   go mod download

   # Run tests
   go test ./...
   ```

3. **Make your changes**

   ```bash
   git checkout -b feature/your-feature-name
   # Make your changes
   git commit -m "feat: add your feature"
   git push origin feature/your-feature-name
   ```

4. **Create Pull Request**
   - Open PR with detailed description
   - Link related issues
   - Wait for review

---

## 💻 Development

### Project Structure

```
bifrost/
├── core/                    # Core Bifrost library
│   ├── schemas/            # Type definitions and interfaces
│   ├── providers/          # AI provider implementations
│   ├── bifrost.go          # Main library code
│   ├── mcp.go             # MCP integration
│   └── logger.go          # Logging utilities
├── transports/             # HTTP transport and integrations
│   └── bifrost-http/      # HTTP server implementation
├── plugins/               # Plugin implementations
├── tests/                 # Integration and end-to-end tests
├── docs/                  # Documentation
└── scripts/              # Build and deployment scripts
```

### Development Setup

<details>
<summary><strong>🔧 Local Development Environment</strong></summary>

**Prerequisites:**

- Go 1.23+
- Docker (for testing)
- Git

**Setup Commands:**

```bash
# Clone repository
git clone https://github.com/maximhq/bifrost.git
cd bifrost

# Install dependencies
go mod download

# Run core tests
cd core && go test ./...

# Run HTTP transport tests
cd transports && go test ./...

# Build HTTP transport
cd transports && go build -o bifrost-http

# Test with Docker
docker build -t bifrost-dev .
docker run -p 8080:8080 bifrost-dev
```

</details>

### Coding Standards

<details>
<summary><strong>📋 Go Code Standards</strong></summary>

**General Guidelines:**

- Follow [Effective Go](https://golang.org/doc/effective_go.html)
- Use `gofmt` for formatting
- Run `golint` and address warnings
- Add tests for new functionality
- Document public APIs with comments

**Specific Rules:**

```go
// ✅ Good: Clear, descriptive names
func (p *OpenAIProvider) ChatCompletion(ctx context.Context, model, key string, messages []BifrostMessage, params *ModelParameters) (*BifrostResponse, *BifrostError)

// ❌ Bad: Unclear abbreviations
func (p *OAIP) CC(c context.Context, m, k string, msgs []BMsg, p *MP) (*BR, *BE)

// ✅ Good: Proper error handling
result, err := provider.ChatCompletion(ctx, model, key, messages, params)
if err != nil {
    return nil, &schemas.BifrostError{
        IsBifrostError: true,
        Error: schemas.ErrorField{
            Message: "failed to complete chat",
            Error:   err,
        },
    }
}

// ✅ Good: Comprehensive logging
logger.Debug(fmt.Sprintf("Making request to %s with model %s", provider, model))
```

</details>

---

## 🧪 Testing

### Test Categories

| Test Type             | Location                         | Purpose                    | Run Command                          |
| --------------------- | -------------------------------- | -------------------------- | ------------------------------------ |
| **Unit Tests**        | `core/`                          | Test individual functions  | `go test ./core/...`                 |
| **Integration Tests** | `tests/core-providers/`          | Test provider integrations | `go test ./tests/core-providers/...` |
| **HTTP Tests**        | `tests/transports-integrations/` | Test HTTP API              | `python -m pytest tests/`            |
| **Plugin Tests**      | `plugins/*/`                     | Test plugin functionality  | `go test ./plugins/...`              |

### Running Tests

<details>
<summary><strong>🧪 Test Execution</strong></summary>

**Core Library Tests:**

```bash
# Run all core tests
cd core && go test ./...

# Run specific provider tests
cd core && go test ./providers -v

# Run with coverage
cd core && go test ./... -coverprofile=coverage.out
go tool cover -html=coverage.out
```

**Integration Tests:**

```bash
# Provider integration tests
cd tests/core-providers
go test -v

# HTTP integration tests
cd tests/transports-integrations
python -m pytest tests/ -v
```

**Performance Tests:**

```bash
# Run benchmarks
cd core && go test -bench=. -benchmem

# Load testing
cd tests && go run load-test.go
```

</details>

### Writing Tests

<details>
<summary><strong>✏️ Test Writing Guidelines</strong></summary>

**Unit Test Example:**

```go
func TestOpenAIProvider_ChatCompletion(t *testing.T) {
    // Setup
    provider := &OpenAIProvider{
        logger: &MockLogger{},
        client: &MockHTTPClient{},
    }

    // Test data
    messages := []schemas.BifrostMessage{
        {Role: "user", Content: "Hello"},
    }

    // Execute
    result, err := provider.ChatCompletion(context.Background(), "gpt-4o-mini", "test-key", messages, nil)

    // Assert
    assert.NoError(t, err)
    assert.NotNil(t, result)
    assert.Equal(t, "gpt-4o-mini", result.Model)
}
```

**Integration Test Example:**

```go
func TestEndToEndChatCompletion(t *testing.T) {
    // Skip if no API keys
    if os.Getenv("OPENAI_API_KEY") == "" {
        t.Skip("OPENAI_API_KEY not set")
    }

    // Setup Bifrost
    client, err := bifrost.Init(schemas.BifrostConfig{
        Account: &TestAccount{},
    })
    require.NoError(t, err)
    defer client.Cleanup()

    // Test request
    result, err := client.ChatCompletionRequest(context.Background(), &schemas.BifrostRequest{
        Provider: schemas.OpenAI,
        Model:    "gpt-4o-mini",
        Input: schemas.RequestInput{
            ChatCompletionInput: &[]schemas.BifrostMessage{
                {Role: "user", Content: "Say hello"},
            },
        },
    })

    // Verify response
    assert.NoError(t, err)
    assert.Contains(t, result.Output.Message, "hello")
}
```

</details>

---

## 📝 Documentation

### Documentation Standards

All documentation should be:

- **Clear and concise** - Easy to understand
- **Comprehensive** - Cover all use cases
- **Up-to-date** - Reflect current functionality
- **Well-formatted** - Use consistent markdown styling
- **Searchable** - Include relevant keywords

### Documentation Types

<details>
<summary><strong>📚 Documentation Categories</strong></summary>

**User Documentation:**

- Quick start guides
- Configuration examples
- API references
- Troubleshooting guides

**Developer Documentation:**

- Code architecture explanations
- Contributing guidelines
- Development setup instructions
- Testing procedures

**API Documentation:**

- Function signatures
- Parameter descriptions
- Return value explanations
- Usage examples

</details>

### Writing Guidelines

<details>
<summary><strong>✍️ Documentation Style Guide</strong></summary>

**Structure:**

````markdown
# Title (use sentence case)

Brief description of what this covers.

## Table of Contents

- [Section 1](#section-1)
- [Section 2](#section-2)

## Section 1

### Subsection

Content with examples:

```go
// Code example with comments
func ExampleFunction() {
    // Explanation of what this does
}
```
````

**Key points:**

- Use bullet points for lists
- Use tables for comparisons
- Use code blocks for examples
- Use callouts for important information

````

**Code Examples:**
- Always include working code
- Add comments explaining complex parts
- Show both Go package and HTTP transport usage
- Include error handling examples

</details>

---

## 🎨 Code Style

### Go Style Guidelines

<details>
<summary><strong>🎯 Style Requirements</strong></summary>

**Formatting:**
```bash
# Format all Go code
gofmt -w .

# Organize imports
goimports -w .

# Run linter
golangci-lint run
````

**Naming Conventions:**

- **Packages**: lowercase, single word
- **Functions**: CamelCase, descriptive
- **Variables**: camelCase, meaningful names
- **Constants**: UPPER_CASE or CamelCase for unexported

**Error Handling:**

```go
// ✅ Good: Descriptive error messages
if err != nil {
    return nil, fmt.Errorf("failed to parse response from %s: %w", provider, err)
}

// ❌ Bad: Generic error messages
if err != nil {
    return nil, err
}
```

**Documentation:**

```go
// ✅ Good: Clear function documentation
// ChatCompletion performs a chat completion request to the specified AI provider.
// It handles authentication, request formatting, and response parsing.
// Returns a BifrostResponse on success or BifrostError on failure.
func (b *Bifrost) ChatCompletion(ctx context.Context, req *BifrostRequest) (*BifrostResponse, *BifrostError) {
    // Implementation
}
```

</details>

---

## 🔄 Pull Request Process

### PR Checklist

Before submitting a pull request:

- [ ] **Tests pass**: `go test ./...`
- [ ] **Code formatted**: `gofmt -w .`
- [ ] **Linting clean**: `golangci-lint run`
- [ ] **Documentation updated**: If adding features
- [ ] **Changelog updated**: Add entry to CHANGELOG.md
- [ ] **Issue linked**: Reference related issues

### PR Template

```markdown
## Description

Brief description of changes made.

## Type of Change

- [ ] Bug fix (non-breaking change)
- [ ] New feature (non-breaking change)
- [ ] Breaking change (fix or feature that would cause existing functionality to not work as expected)
- [ ] Documentation update

## Testing

- [ ] Unit tests added/updated
- [ ] Integration tests pass
- [ ] Manual testing completed

## Related Issues

Fixes #(issue number)

## Additional Notes

Any additional information for reviewers.
```

### Review Process

1. **Automated Checks**: CI/CD runs tests and linting
2. **Code Review**: Maintainers review code quality and design
3. **Testing**: Additional testing in staging environment
4. **Approval**: Two approvals required for merge
5. **Merge**: Squash and merge to main branch

---

## 🐛 Bug Reports

### Bug Report Template

```markdown
**Bug Description**
A clear and concise description of what the bug is.

**Steps to Reproduce**

1. Set up Bifrost with configuration X
2. Make request Y
3. Observe behavior Z

**Expected Behavior**
What should have happened.

**Actual Behavior**
What actually happened.

**Environment**

- Bifrost version:
- Go version:
- OS:
- Provider:

**Logs**
Include relevant log output (remove sensitive data).

**Additional Context**
Any other information that might help.
```

### Debugging Help

Before reporting bugs:

1. **Check existing issues**: Search for similar problems
2. **Enable debug logging**: Use `LogLevelDebug`
3. **Test minimal case**: Isolate the problem
4. **Gather information**: Version, config, logs
5. **Remove sensitive data**: API keys, personal information

---

## 💡 Feature Requests

### Feature Request Template

```markdown
**Feature Description**
Clear description of the proposed feature.

**Use Case**
Why would this feature be useful? What problem does it solve?

**Proposed Solution**
How do you think this should be implemented?

**Alternatives Considered**
Other ways to solve this problem.

**Additional Context**
Any other relevant information.
```

### Feature Development Process

1. **Discussion**: Discuss feature in GitHub issues
2. **Design**: Create design document if complex
3. **Approval**: Get maintainer approval before coding
4. **Implementation**: Develop with tests and documentation
5. **Review**: Submit PR following guidelines
6. **Release**: Feature included in next release

---

## 🏆 Recognition

Contributors are recognized in:

- **CONTRIBUTORS.md**: All contributors listed
- **Release Notes**: Major contributors highlighted
- **GitHub**: Contributor graph and statistics
- **Discord**: Special contributor role (coming soon)

---

## 📞 Community & Support

- **GitHub Discussions**: General questions and ideas
- **GitHub Issues**: Bug reports and feature requests
- **Discord**: Real-time chat (coming soon)
- **Email**: [contact@maxim.ai](mailto:contact@maxim.ai) for sensitive issues

---

**Thank you for contributing to Bifrost!** 🎉

Every contribution, no matter how small, helps make AI integration easier for developers worldwide.
