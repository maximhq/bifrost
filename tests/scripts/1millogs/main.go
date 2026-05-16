package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var (
	// Command line flags
	dbType       = flag.String("db", "sqlite", "Database type: sqlite or postgres")
	dbPath       = flag.String("path", "./tests/configs/default/logs.db", "Path to SQLite database")
	dbHost       = flag.String("host", "localhost", "PostgreSQL host")
	dbPort       = flag.Int("port", 5432, "PostgreSQL port")
	dbUser       = flag.String("user", "postgres", "PostgreSQL user")
	dbPassword   = flag.String("password", "", "PostgreSQL password")
	dbName       = flag.String("dbname", "bifrost_logs", "PostgreSQL database name")
	numRows      = flag.Int("rows", 1000000, "Number of rows to insert")
	batchSize    = flag.Int("batch", 1000, "Batch size for inserts")
	targetSizeGB = flag.Float64("size", 17.5, "Target size in GB (will adjust row size)")
	logType      = flag.String("type", "llm", "Log type to generate: llm or mcp")
	invalidMeta  = flag.Bool("invalid-metadata", false, "When set, all logs will have invalid JSON metadata")
)

// Providers and models for variety
var providers = []string{"openai", "anthropic", "cohere", "azure", "gemini", "mistral"}
var models = map[string][]string{
	"openai":    {"gpt-4o", "gpt-4o-mini", "gpt-4-turbo", "gpt-3.5-turbo"},
	"anthropic": {"claude-3-5-sonnet-20241022", "claude-3-opus-20240229", "claude-3-haiku-20240307"},
	"cohere":    {"command-r-plus", "command-r", "command"},
	"azure":     {"gpt-4o", "gpt-35-turbo"},
	"gemini":    {"gemini-1.5-pro", "gemini-1.5-flash", "gemini-pro"},
	"mistral":   {"mistral-large-2411", "mistral-medium", "mistral-small"},
}

var statuses = []string{"success", "error", "processing"}
var objects = []string{"chat.completion", "text.completion", "embedding"}

// validMetadataValues are realistic metadata samples used by ~15% of generated logs.
var validMetadataValues = []string{
	`{"environment": "production", "version": "1.0.0"}`,
	`{"team": "backend", "project": "api-gateway"}`,
	`{"user_id": "user-123", "session": "sess-abc"}`,
	`{"source": "web", "region": "us-east-1", "tier": "premium"}`,
	`{"app": "mobile", "os": "ios", "release": "2.4.1"}`,
}

// invalidMetadataValues are malformed JSON strings used when --invalid-metadata is set.
var invalidMetadataValues = []string{
	`{"environment": "production", "version":}`,
	`{"team": "backend", "project"`,
	`{not_quoted_key: "value"}`,
	`{"key": "value",}`,
	`{"nested": {"a": 1, "b": [2, 3}}}`,
	`just plain text, not json at all`,
	`<xml>not json</xml>`,
	`{"unterminated": "string value`,
}

// MCP tool names and server labels for MCP log generation
var mcpToolNames = []string{"get_weather", "search_docs", "execute_query", "send_email", "create_ticket", "fetch_url", "list_files", "run_code"}
var mcpServerLabels = []string{"weather-service", "doc-search", "db-query", "email-service", "ticketing", "web-fetcher", "file-manager", "code-runner"}

var mcpToolArguments = []map[string]interface{}{
	{"city": "San Francisco", "units": "celsius"},
	{"query": "kubernetes deployment best practices", "limit": 10},
	{"sql": "SELECT * FROM users WHERE active = true LIMIT 100", "database": "analytics"},
	{"to": "team@example.com", "subject": "Deploy complete", "body": "v2.4.1 deployed successfully"},
	{"title": "Fix login timeout", "priority": "high", "assignee": "backend-team"},
	{"url": "https://api.example.com/health", "method": "GET", "timeout": 5000},
	{"path": "/var/log/app", "pattern": "*.log", "recursive": true},
	{"language": "python", "code": "print('hello world')", "timeout": 30},
}

var mcpToolResults = []map[string]interface{}{
	{"temperature": 18.5, "condition": "partly cloudy", "humidity": 65},
	{"results": []string{"doc-1", "doc-2", "doc-3"}, "total": 42},
	{"rows_affected": 156, "execution_time_ms": 23},
	{"message_id": "msg-abc123", "status": "sent"},
	{"ticket_id": "TICK-4521", "url": "https://tickets.example.com/TICK-4521"},
	{"status_code": 200, "body": "{\"status\":\"healthy\"}", "latency_ms": 45},
	{"files": []string{"app.log", "error.log", "access.log"}, "count": 3},
	{"stdout": "hello world\n", "exit_code": 0, "duration_ms": 150},
}

var mcpErrorMessages = []string{
	"Connection timeout after 30s",
	"Permission denied: insufficient scope",
	"Rate limit exceeded: 100 requests/min",
	"Resource not found: document-id-xyz",
	"Internal server error in downstream service",
	"Invalid arguments: missing required field 'query'",
}

// Sample conversation content for realistic data
var userPrompts = []string{
	"Explain quantum computing in simple terms.",
	"Write a Python function to calculate fibonacci numbers.",
	"What are the best practices for React application architecture?",
	"How does blockchain technology work?",
	"Explain the difference between SQL and NoSQL databases.",
	"What are microservices and when should I use them?",
	"How do I optimize database queries for better performance?",
	"Explain machine learning model training process.",
	"What is the CAP theorem in distributed systems?",
	"How does OAuth 2.0 authentication work?",
	"Explain the SOLID principles in software engineering.",
	"What are the key differences between REST and GraphQL?",
	"How do I implement caching strategies in web applications?",
	"Explain Docker containers and their benefits.",
	"What is the difference between synchronous and asynchronous programming?",
}

var assistantResponses = []string{
	`Quantum computing is a revolutionary approach to computation that leverages quantum mechanical phenomena. Unlike classical computers that use bits (0 or 1), quantum computers use quantum bits or "qubits" that can exist in multiple states simultaneously through superposition. This allows them to process vast amounts of information in parallel. Key concepts include:

1. **Superposition**: Qubits can be in multiple states at once, unlike classical bits which are either 0 or 1.
2. **Entanglement**: Qubits can be correlated with each other in ways that classical bits cannot.
3. **Interference**: Quantum algorithms use interference to amplify correct answers and cancel out wrong ones.

Quantum computers excel at specific tasks like:
- Cryptography and code breaking
- Optimization problems
- Molecular simulation
- Machine learning

However, they face challenges like decoherence (losing quantum state) and error correction. Current quantum computers are in the NISQ (Noisy Intermediate-Scale Quantum) era, meaning they're still developing and prone to errors.`,

	`Here's a comprehensive Python implementation for calculating Fibonacci numbers with multiple approaches:

` + "```python" + `
def fibonacci_iterative(n):
    """Calculate nth Fibonacci number iteratively - O(n) time, O(1) space"""
    if n <= 1:
        return n
    
    prev, curr = 0, 1
    for _ in range(2, n + 1):
        prev, curr = curr, prev + curr
    return curr

def fibonacci_recursive(n, memo=None):
    """Calculate nth Fibonacci with memoization - O(n) time and space"""
    if memo is None:
        memo = {}
    
    if n in memo:
        return memo[n]
    
    if n <= 1:
        return n
    
    memo[n] = fibonacci_recursive(n-1, memo) + fibonacci_recursive(n-2, memo)
    return memo[n]

def fibonacci_generator(limit):
    """Generate Fibonacci sequence up to limit"""
    a, b = 0, 1
    while a < limit:
        yield a
        a, b = b, a + b

# Usage examples
print(fibonacci_iterative(10))  # Output: 55
print(fibonacci_recursive(10))  # Output: 55

# Generate sequence
for num in fibonacci_generator(100):
    print(num, end=' ')
` + "```" + `

The iterative approach is most efficient for single calculations, while the generator is best for sequences.`,

	`React application architecture best practices involve several key principles and patterns:

**1. Component Organization**
- Feature-based folder structure over type-based
- Separate presentational from container components
- Keep components small and focused (Single Responsibility)

**2. State Management**
- Use Context API for simple global state
- Redux/Zustand for complex state management
- React Query for server state
- Keep state as local as possible

**3. Code Structure**
` + "```" + `
src/
├── features/          # Feature-based modules
│   ├── auth/
│   ├── dashboard/
│   └── profile/
├── components/        # Shared components
├── hooks/            # Custom hooks
├── utils/            # Helper functions
├── services/         # API calls
└── contexts/         # Context providers
` + "```" + `

**4. Performance Optimization**
- Use React.memo() for expensive components
- Implement code splitting with lazy loading
- Optimize re-renders with useMemo and useCallback
- Use virtualization for long lists

**5. Testing Strategy**
- Unit tests for utilities and hooks
- Integration tests for features
- E2E tests for critical user flows

**6. Error Handling**
- Error boundaries for graceful failures
- Proper loading and error states
- User-friendly error messages`,

	`Blockchain technology is a distributed ledger system that maintains a continuously growing list of records called blocks. Here's how it works:

**Core Concepts:**

1. **Blocks**: Each block contains:
   - Transaction data
   - Timestamp
   - Hash of previous block
   - Cryptographic hash of current block

2. **Chain Structure**: Blocks are linked chronologically, forming an immutable chain. Changing any block would invalidate all subsequent blocks.

3. **Decentralization**: Instead of a central authority, the blockchain is distributed across a network of nodes (computers).

**How It Works:**

1. **Transaction Initiation**: User requests a transaction
2. **Broadcasting**: Transaction is broadcast to all nodes in the network
3. **Validation**: Nodes validate the transaction using consensus mechanisms
4. **Block Creation**: Valid transactions are combined into a new block
5. **Mining/Consensus**: Nodes compete to validate the block (Proof of Work) or use other consensus mechanisms (Proof of Stake)
6. **Addition to Chain**: Once validated, the block is added to the chain
7. **Distribution**: Updated blockchain is distributed across the network

**Key Features:**
- **Immutability**: Once recorded, data cannot be altered retroactively
- **Transparency**: All transactions are visible to network participants
- **Security**: Cryptographic hashing makes tampering extremely difficult
- **Decentralization**: No single point of failure or control

**Use Cases:**
- Cryptocurrencies (Bitcoin, Ethereum)
- Supply chain tracking
- Smart contracts
- Digital identity
- Healthcare records`,

	`SQL and NoSQL databases serve different purposes and have distinct characteristics:

**SQL Databases (Relational)**

*Structure:*
- Fixed schema with tables, rows, and columns
- Relationships defined through foreign keys
- ACID compliant (Atomicity, Consistency, Isolation, Durability)

*Examples:* PostgreSQL, MySQL, Oracle, SQL Server

*Best For:*
- Complex queries and joins
- Transactions requiring consistency
- Structured data with clear relationships
- Applications requiring referential integrity

*Pros:*
- Mature technology with extensive tools
- Strong consistency guarantees
- Powerful query language (SQL)
- Good for complex relationships

*Cons:*
- Less flexible schema
- Vertical scaling challenges
- Can be slower for simple key-value operations

**NoSQL Databases (Non-Relational)**

*Types:*
1. Document stores (MongoDB, Couchbase)
2. Key-value stores (Redis, DynamoDB)
3. Column-family stores (Cassandra, HBase)
4. Graph databases (Neo4j, Amazon Neptune)

*Best For:*
- Flexible, evolving schemas
- Horizontal scaling needs
- High-velocity data ingestion
- Unstructured or semi-structured data

*Pros:*
- Flexible schema design
- Excellent horizontal scalability
- High performance for specific use cases
- Better for hierarchical data

*Cons:*
- Limited query capabilities
- Eventual consistency (in many cases)
- Lack of standardization
- Fewer tools and expertise available

**Choosing Between Them:**
- Use SQL when you need complex queries, transactions, and relationships
- Use NoSQL when you need flexibility, scalability, and high performance for specific patterns
- Many modern applications use both (polyglot persistence)`,
}

func main() {
	flag.Parse()

	if *logType != "llm" && *logType != "mcp" {
		fmt.Printf("❌ Invalid log type: %s (must be 'llm' or 'mcp')\n", *logType)
		os.Exit(1)
	}

	fmt.Println("🚀 Bifrost Logs Population Script")
	fmt.Println("==================================")
	fmt.Printf("Log Type: %s\n", *logType)
	fmt.Printf("Invalid Metadata: %v\n", *invalidMeta)
	fmt.Printf("Database Type: %s\n", *dbType)
	fmt.Printf("Target Rows: %d\n", *numRows)
	fmt.Printf("Target Size: %.2f GB\n", *targetSizeGB)
	fmt.Printf("Batch Size: %d\n", *batchSize)
	fmt.Println()

	// Calculate target size per row
	targetBytesPerRow := int((*targetSizeGB * 1024 * 1024 * 1024) / float64(*numRows))
	fmt.Printf("📊 Target size per row: ~%d bytes (%.2f KB)\n\n", targetBytesPerRow, float64(targetBytesPerRow)/1024)

	// Connect to database
	db, err := connectDB()
	if err != nil {
		fmt.Printf("❌ Failed to connect to database: %v\n", err)
		os.Exit(1)
	}

	// Run migrations
	fmt.Println("🔄 Running migrations...")

	sqlDB, err := db.DB()
	if err != nil {
		fmt.Printf("❌ Failed to get DB instance: %v\n", err)
		os.Exit(1)
	}

	// Create table based on log type
	if *logType == "mcp" {
		if err := db.AutoMigrate(&logstore.MCPToolLog{}); err != nil {
			fmt.Printf("❌ Failed to migrate: %v\n", err)
			os.Exit(1)
		}
	} else {
		if err := db.AutoMigrate(&logstore.Log{}); err != nil {
			fmt.Printf("❌ Failed to migrate: %v\n", err)
			os.Exit(1)
		}
	}

	fmt.Println("✅ Database ready")
	fmt.Println()

	// Get current count
	var currentCount int64
	if *logType == "mcp" {
		db.Model(&logstore.MCPToolLog{}).Count(&currentCount)
	} else {
		db.Model(&logstore.Log{}).Count(&currentCount)
	}
	fmt.Printf("📈 Current rows in database: %d\n", currentCount)
	fmt.Printf("🎯 Will insert %d new rows\n\n", *numRows)

	// Generate and insert logs
	rand.Seed(time.Now().UnixNano())

	startTime := time.Now()
	totalInserted := 0

	fmt.Println("⏳ Starting insertion...")
	fmt.Println()

	for i := 0; i < *numRows; i += *batchSize {
		batchEnd := i + *batchSize
		if batchEnd > *numRows {
			batchEnd = *numRows
		}

		if *logType == "mcp" {
			batch := make([]logstore.MCPToolLog, batchEnd-i)
			for j := range batch {
				batch[j] = generateMCPLog(i + j)
			}
			if err := db.Create(&batch).Error; err != nil {
				fmt.Printf("❌ Error inserting batch at row %d: %v\n", i, err)
				os.Exit(1)
			}
		} else {
			batch := make([]logstore.Log, batchEnd-i)
			for j := range batch {
				batch[j] = generateLog(i+j, targetBytesPerRow)
			}
			if err := db.Create(&batch).Error; err != nil {
				fmt.Printf("❌ Error inserting batch at row %d: %v\n", i, err)
				os.Exit(1)
			}
		}

		totalInserted += batchEnd - i

		// Progress update
		if (i+*batchSize)%(*batchSize*10) == 0 || batchEnd == *numRows {
			elapsed := time.Since(startTime)
			progress := float64(totalInserted) / float64(*numRows) * 100
			rate := float64(totalInserted) / elapsed.Seconds()
			remaining := time.Duration(float64(*numRows-totalInserted)/rate) * time.Second

			fmt.Printf("\r📊 Progress: %d/%d (%.1f%%) | Rate: %.0f rows/s | Elapsed: %s | ETA: %s",
				totalInserted, *numRows, progress, rate,
				elapsed.Round(time.Second), remaining.Round(time.Second))
		}
	}

	fmt.Println()
	fmt.Println()

	// Final statistics
	elapsed := time.Since(startTime)

	var finalCount int64
	if *logType == "mcp" {
		db.Model(&logstore.MCPToolLog{}).Count(&finalCount)
	} else {
		db.Model(&logstore.Log{}).Count(&finalCount)
	}

	// Get database size
	var dbSize int64
	if *dbType == "sqlite" {
		fileInfo, err := os.Stat(*dbPath)
		if err == nil {
			dbSize = fileInfo.Size()
		}
	} else {
		// For PostgreSQL, query the database size
		db.Raw("SELECT pg_database_size(current_database())").Scan(&dbSize)
	}

	dbSizeGB := float64(dbSize) / (1024 * 1024 * 1024)

	fmt.Println("✅ Insertion Complete!")
	fmt.Println("======================")
	fmt.Printf("📊 Total rows inserted: %d\n", totalInserted)
	fmt.Printf("📊 Total rows in database: %d\n", finalCount)
	fmt.Printf("⏱️  Total time: %s\n", elapsed.Round(time.Second))
	fmt.Printf("⚡ Average rate: %.0f rows/second\n", float64(totalInserted)/elapsed.Seconds())
	fmt.Printf("💾 Database size: %.2f GB\n", dbSizeGB)
	fmt.Printf("📏 Average row size: %.2f KB\n", float64(dbSize)/float64(finalCount)/1024)

	if dbSizeGB < *targetSizeGB*0.9 || dbSizeGB > *targetSizeGB*1.1 {
		fmt.Printf("\n⚠️  Database size (%.2f GB) is outside target range (%.2f GB ±10%%)\n", dbSizeGB, *targetSizeGB)
		fmt.Println("💡 Adjust the --size parameter and run again to fine-tune")
	}

	sqlDB.Close()
	fmt.Println("\n🎉 Done!")
}

func connectDB() (*gorm.DB, error) {
	config := &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	}

	switch *dbType {
	case "sqlite":
		// Ensure directory exists
		dir := filepath.Dir(*dbPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create directory: %w", err)
		}

		db, err := gorm.Open(sqlite.Open(*dbPath), config)
		if err != nil {
			return nil, fmt.Errorf("failed to open SQLite database: %w", err)
		}

		// Optimize SQLite for bulk inserts
		sqlDB, err := db.DB()
		if err != nil {
			return nil, err
		}
		sqlDB.Exec("PRAGMA journal_mode=WAL")
		sqlDB.Exec("PRAGMA synchronous=NORMAL")
		sqlDB.Exec("PRAGMA cache_size=1000000")
		sqlDB.Exec("PRAGMA temp_store=MEMORY")

		return db, nil

	case "postgres":
		dsn := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable",
			*dbHost, *dbPort, *dbUser, *dbPassword, *dbName)

		db, err := gorm.Open(postgres.Open(dsn), config)
		if err != nil {
			return nil, fmt.Errorf("failed to open PostgreSQL database: %w", err)
		}

		return db, nil

	default:
		return nil, fmt.Errorf("unsupported database type: %s", *dbType)
	}
}

func generateLog(index int, targetSize int) logstore.Log {
	// Select random provider and model
	provider := providers[rand.Intn(len(providers))]
	modelList := models[provider]
	model := modelList[rand.Intn(len(modelList))]
	status := statuses[rand.Intn(len(statuses))]
	object := objects[rand.Intn(len(objects))]

	// Generate timestamps
	timestamp := time.Now().Add(-time.Duration(rand.Intn(90*24)) * time.Hour) // Random time in last 90 days

	// Generate chat history - scale based on target size
	numMessages := 2 + rand.Intn(5) // 2-6 messages
	inputHistory := make([]schemas.ChatMessage, numMessages)

	// Create substantial messages to reach target size
	baseMessageSize := targetSize / (numMessages + 1) // +1 for output message

	for i := range inputHistory {
		var content string
		var role schemas.ChatMessageRole

		if i%2 == 0 {
			role = schemas.ChatMessageRoleUser
			content = userPrompts[rand.Intn(len(userPrompts))]
			// Pad user message if needed
			content = padContent(content, baseMessageSize/2)
		} else {
			role = schemas.ChatMessageRoleAssistant
			content = assistantResponses[rand.Intn(len(assistantResponses))]
			// Pad assistant message if needed
			content = padContent(content, baseMessageSize)
		}

		inputHistory[i] = schemas.ChatMessage{
			Role: role,
			Content: &schemas.ChatMessageContent{
				ContentStr: &content,
			},
		}
	}

	// Generate output message
	outputContent := assistantResponses[rand.Intn(len(assistantResponses))]
	outputContent = padContent(outputContent, baseMessageSize)

	outputMessage := schemas.ChatMessage{
		Role: schemas.ChatMessageRoleAssistant,
		Content: &schemas.ChatMessageContent{
			ContentStr: &outputContent,
		},
	}

	// Serialize to JSON
	inputHistoryJSON, _ := json.Marshal(inputHistory)
	outputMessageJSON, _ := json.Marshal(outputMessage)

	// Token usage
	promptTokens := rand.Intn(2000) + 500
	completionTokens := rand.Intn(1500) + 300
	totalTokens := promptTokens + completionTokens

	tokenUsage := schemas.BifrostLLMUsage{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      totalTokens,
	}
	tokenUsageJSON, _ := json.Marshal(tokenUsage)

	// Generate latency and cost
	latency := float64(rand.Intn(3000)+100) / 1000.0 // 0.1 to 3.1 seconds
	cost := float64(totalTokens) * 0.00001           // Rough estimate

	// Create content summary for search
	contentSummary := fmt.Sprintf("%s %s",
		userPrompts[rand.Intn(len(userPrompts))],
		assistantResponses[rand.Intn(len(assistantResponses))][:min(200, len(assistantResponses[0]))])

	// Generate log entry
	log := logstore.Log{
		ID:               fmt.Sprintf("log-%d-%d", timestamp.Unix(), index),
		Timestamp:        timestamp,
		Object:           object,
		Provider:         provider,
		Model:            model,
		Status:           status,
		Stream:           rand.Float32() > 0.5,
		InputHistory:     string(inputHistoryJSON),
		OutputMessage:    string(outputMessageJSON),
		TokenUsage:       string(tokenUsageJSON),
		Latency:          &latency,
		Cost:             &cost,
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      totalTokens,
		ContentSummary:   contentSummary,
		CreatedAt:        timestamp,
		NumberOfRetries:  rand.Intn(3),
		FallbackIndex:    rand.Intn(2),
	}

	// Add error details for failed requests
	if status == "error" {
		errorDetails := schemas.BifrostError{
			Error: &schemas.ErrorField{
				Message: "Rate limit exceeded",
			},
			Type: bifrost.Ptr("rate_limit_error"),
		}
		errorJSON, _ := json.Marshal(errorDetails)
		log.ErrorDetails = string(errorJSON)
	}

	// Assign metadata: all invalid when --invalid-metadata is set, otherwise ~15% valid.
	if *invalidMeta {
		log.Metadata = new(invalidMetadataValues[rand.Intn(len(invalidMetadataValues))])
	} else if rand.Float32() < 0.15 {
		log.Metadata = new(validMetadataValues[rand.Intn(len(validMetadataValues))])
	}

	// Randomly add selected key and virtual key
	if rand.Float32() > 0.3 {
		log.SelectedKeyID = fmt.Sprintf("key-%d", rand.Intn(100))
		log.SelectedKeyName = fmt.Sprintf("API Key %d", rand.Intn(100))
	}

	if rand.Float32() > 0.5 {
		virtualKeyID := fmt.Sprintf("vkey-%d", rand.Intn(50))
		virtualKeyName := fmt.Sprintf("Virtual Key %d", rand.Intn(50))
		log.VirtualKeyID = &virtualKeyID
		log.VirtualKeyName = &virtualKeyName
	}

	return log
}

func generateMCPLog(index int) logstore.MCPToolLog {
	toolIdx := rand.Intn(len(mcpToolNames))
	toolName := mcpToolNames[toolIdx]
	serverLabel := mcpServerLabels[toolIdx]
	status := statuses[rand.Intn(len(statuses))]

	timestamp := time.Now().Add(-time.Duration(rand.Intn(90*24)) * time.Hour)

	// Serialize arguments
	args := mcpToolArguments[toolIdx]
	argsJSON, _ := json.Marshal(args)

	// Serialize result (only for non-error statuses)
	var resultJSON []byte
	if status != "error" {
		result := mcpToolResults[toolIdx]
		resultJSON, _ = json.Marshal(result)
	}

	latency := float64(rand.Intn(2000)+50) / 1000.0 // 50ms to 2050ms
	cost := float64(rand.Intn(10)+1) * 0.001         // $0.001 to $0.01

	log := logstore.MCPToolLog{
		ID:          fmt.Sprintf("mcp-%d-%d", timestamp.Unix(), index),
		Timestamp:   timestamp,
		ToolName:    toolName,
		ServerLabel: serverLabel,
		Status:      status,
		Arguments:   string(argsJSON),
		Result:      string(resultJSON),
		Latency:     &latency,
		Cost:        &cost,
		CreatedAt:   timestamp,
	}

	// Add error details for failed requests
	if status == "error" {
		errorDetails := schemas.BifrostError{
			Error: &schemas.ErrorField{
				Message: mcpErrorMessages[rand.Intn(len(mcpErrorMessages))],
			},
			Type: bifrost.Ptr("tool_execution_error"),
		}
		errorJSON, _ := json.Marshal(errorDetails)
		log.ErrorDetails = string(errorJSON)
	}

	// Assign metadata: all invalid when --invalid-metadata is set, otherwise ~15% valid.
	if *invalidMeta {
		log.Metadata = invalidMetadataValues[rand.Intn(len(invalidMetadataValues))]
	} else if rand.Float32() < 0.15 {
		log.Metadata = validMetadataValues[rand.Intn(len(validMetadataValues))]
	}

	// ~50% linked to an LLM request
	if rand.Float32() < 0.5 {
		llmReqID := fmt.Sprintf("log-%d-%d", timestamp.Unix(), rand.Intn(1000))
		log.LLMRequestID = &llmReqID
	}

	// ~70% have a virtual key
	if rand.Float32() < 0.7 {
		vkID := fmt.Sprintf("vkey-%d", rand.Intn(50))
		vkName := fmt.Sprintf("Virtual Key %d", rand.Intn(50))
		log.VirtualKeyID = &vkID
		log.VirtualKeyName = &vkName
	}

	return log
}

// padContent adds additional content to reach target size
func padContent(content string, targetSize int) string {
	currentSize := len(content)
	if currentSize >= targetSize {
		return content
	}

	// Add padding text that looks somewhat realistic
	paddingTexts := []string{
		"\n\nAdditional context: This explanation can be extended with more details about implementation specifics, edge cases, and advanced use cases.",
		"\n\nFor more information, consider exploring related topics such as system design patterns, performance optimization techniques, and scalability considerations.",
		"\n\nReal-world applications of this concept include enterprise systems, distributed architectures, cloud-native applications, and microservices-based solutions.",
		"\n\nBest practices suggest considering factors like maintainability, testability, documentation, error handling, logging, monitoring, and security throughout the development lifecycle.",
		"\n\nWhen implementing this in production environments, pay attention to resource management, connection pooling, caching strategies, rate limiting, and graceful degradation.",
	}

	for len(content) < targetSize {
		padding := paddingTexts[rand.Intn(len(paddingTexts))]
		content += padding

		// Add repeated detailed explanations to fill space
		if len(content) < targetSize {
			content += fmt.Sprintf("\n\nDetailed analysis point %d: ", rand.Intn(100))
			content += "In depth exploration of technical considerations, architectural decisions, implementation details, performance characteristics, scalability factors, and operational requirements. "

			// Calculate remaining space and repeat count
			remaining := targetSize - len(content)
			if remaining > 0 {
				repeatUnit := "This provides comprehensive coverage of the topic with extensive examples and use cases. "
				repeatCount := remaining / len(repeatUnit)
				if repeatCount > 0 {
					content += strings.Repeat(repeatUnit, repeatCount)
				}
			}
		}
	}

	// Truncate to exact target size if we went over
	if len(content) > targetSize {
		return content[:targetSize]
	}

	return content
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
