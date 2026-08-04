# Bifrost File Patterns

## Package and directory layout
```text
.
├── cli/                    Bubble Tea terminal client (separate module)
├── community/              Community-maintained MCP catalog data
├── context/                Session priming references
├── core/                   Gateway engine and public Go SDK
│   ├── internal/           Shared LLM/MCP test harnesses
│   ├── mcp/                MCP clients, tools, and agent loop
│   ├── providers/<name>/   Provider controller, converters, and errors
│   └── schemas/            Shared public contracts and wire types
├── framework/              Reusable runtime and persistence facilities
│   ├── configstore/        ConfigStore interface, GORM stores, table models
│   ├── logstore/           Log stores, retention, async results
│   ├── migrator/           GORM migration runner
│   ├── sidekiq/            DB-backed background-job runner
│   ├── streaming/          Stream accumulation and conversion
│   └── vectorstore/        Redis/Qdrant/Pinecone/Weaviate adapters
├── plugins/<name>/         Independently versioned plugin modules
├── helm-charts/            Kubernetes Helm chart and values
├── nix/                    Nix packages, modules, and development shell
├── npx/                    Node launchers for Bifrost binaries
├── recipes/                Deployment-oriented Make recipes
├── scripts/                Migration CLI and maintenance utilities
├── transports/             Config schema and transport module
│   └── bifrost-http/       FastHTTP executable
│       ├── handlers/       HTTP boundary, validation, responses
│       ├── integrations/   Provider-SDK-compatible HTTP converters
│       ├── lib/            Config, context, middleware, validation
│       └── server/         Bootstrap, wiring, lifecycle, routes
├── ui/                     React/Vite dashboard
├── tests/                  E2E, integration, load, and utility programs
├── docs/                   Mintlify documentation
├── examples/               Runnable integrations and deployments
├── terraform/              Infrastructure modules
├── .claude/                Repository-specific agent skills
└── .github/                CI and release automation
```

## Naming conventions
| Kind | Convention |
|---|---|
| Files | New files use lowercase concatenated words; `_test.go` is allowed; legacy underscore names exist |
| Handler | feature file; `XHandler`, `NewXHandler`, `RegisterRoutes`, unexported verb handler |
| Service | feature file or `service.go`; `Service`, `NewService`, exported domain verb |
| Store/repository | `store.go`, `rdb.go`, backend file; `ConfigStore`, `RDBConfigStore`, CRUD verb |
| Worker | feature file; `XWorker`/`Runner`, `NewX`, `Start`/`Stop`/`Run` |
| Domain type | noun in `core/schemas` or `framework/.../tables`; DB models use `TableX` |
| Interface | capability noun (`Store`, `ModelsManager`), no `I` prefix; usually consumer-side |
| Errors | `ErrX` sentinel or `XError` type, tested with `errors.Is`/`errors.As` |

## Code patterns

### HTTP handler
```go
func (h *SkillsHandler) getAllSkillsVersion(ctx *fasthttp.RequestCtx) {
	version, err := h.store.GetAllSkillsVersion(ctx)
	if err != nil {
		logger.Error("failed to get all-skills version: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to get all-skills version")
		return
	}
	SendJSON(ctx, AllSkillsVersionResponse{Version: version})
}
```

### Service method
```go
func (s *Service) DeleteByResourceID(ctx context.Context, scope, resourceID string) (int64, error) {
	if scope == "" || resourceID == "" {
		return 0, nil
	}
	n, err := s.store.DeleteTempTokensByResourceID(ctx, scope, resourceID)
	if err != nil {
		return 0, fmt.Errorf("temptoken: delete by resource_id failed: %w", err)
	}
	return n, nil
}
```

### Store method
```go
func (s *RDBConfigStore) GetSidekiqJob(ctx context.Context, id string) (*tables.TableSidekiqJob, error) {
	var job tables.TableSidekiqJob
	err := s.DB().WithContext(ctx).Where("id = ?", id).First(&job).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &job, nil
}
```

### Domain error
```go
var ErrNotFound = errors.New("not found")

type ErrUnresolvedKeys struct {
	Identifiers []string
}

func (e *ErrUnresolvedKeys) Error() string {
	return fmt.Sprintf("could not resolve keys: %s", strings.Join(e.Identifiers, ", "))
}
```

### Table-driven test
```go
func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{{name: "valid", input: "value"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parse(tt.input)
			require.Equal(t, tt.wantErr, err != nil)
		})
	}
}
```
