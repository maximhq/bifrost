// Package main provides a tool to validate migration table creation order
// by checking that dependent tables (with foreign keys) are created after
// the tables they reference.
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// TableDependency represents a foreign key relationship where
// Table has a FK column that references DependsOn
type TableDependency struct {
	Table      string // Table that has the FK column
	DependsOn  string // Table being referenced (must be created first)
	Field      string // Field name with the FK
	SourceFile string // Source file where this is defined
}

// MigrationAction represents a table creation or column addition in migrations
type MigrationAction struct {
	MigrationID string
	ActionType  string // "CreateTable" or "AddColumn"
	Table       string
	Column      string // Only for AddColumn
	Order       int    // Order within migrations.go
	Line        int    // Line number in file
}

func main() {
	// Default paths relative to where the script is run from
	migrationsPath := "framework/configstore/migrations.go"
	tablesDir := "framework/configstore/tables"

	// Allow overriding via command line args
	if len(os.Args) > 1 {
		migrationsPath = os.Args[1]
	}
	if len(os.Args) > 2 {
		tablesDir = os.Args[2]
	}

	// Parse table definitions to get FK dependencies
	dependencies, err := parseTableDefinitions(tablesDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing table definitions: %v\n", err)
		os.Exit(1)
	}

	// Parse migrations to get table creation order
	actions, err := parseMigrationOrder(migrationsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing migrations: %v\n", err)
		os.Exit(1)
	}

	// Validate dependencies
	violations := validateDependencies(dependencies, actions)

	// Report results
	if len(violations) == 0 {
		fmt.Println("✓ All migration dependencies are satisfied!")
		fmt.Printf("  Checked %d table dependencies across %d migration actions\n", len(dependencies), len(actions))
		os.Exit(0)
	}

	fmt.Printf("✗ Found %d dependency violation(s):\n\n", len(violations))
	for _, v := range violations {
		fmt.Println(v)
	}
	os.Exit(1)
}

// parseTableDefinitions parses all Go files in the tables directory
// and extracts foreign key relationships from GORM struct tags
//
// GORM FK relationships:
// 1. Belongs-to: `Budget *TableBudget gorm:"foreignKey:BudgetID"` 
//    - FK column (BudgetID) is on THIS table
//    - Referenced table (TableBudget) must be created FIRST
//
// 2. Has-many: `Keys []TableKey gorm:"foreignKey:ProviderID"`
//    - FK column (ProviderID) is on the CHILD table (TableKey)
//    - THIS table (parent) must be created FIRST
//    - We don't track this as a dependency because the parent comes first naturally
func parseTableDefinitions(tablesDir string) ([]TableDependency, error) {
	var dependencies []TableDependency

	// Table struct name to table name mapping
	tableNames := make(map[string]string)

	// First pass: collect all table name mappings
	files, err := filepath.Glob(filepath.Join(tablesDir, "*.go"))
	if err != nil {
		return nil, fmt.Errorf("failed to glob tables dir: %w", err)
	}

	fset := token.NewFileSet()
	for _, file := range files {
		node, err := parser.ParseFile(fset, file, nil, parser.ParseComments)
		if err != nil {
			return nil, fmt.Errorf("failed to parse %s: %w", file, err)
		}

		// Find struct types and their TableName methods
		ast.Inspect(node, func(n ast.Node) bool {
			// Look for TableName() methods
			if funcDecl, ok := n.(*ast.FuncDecl); ok {
				if funcDecl.Name.Name == "TableName" && funcDecl.Recv != nil {
					// Get the receiver type name
					if len(funcDecl.Recv.List) > 0 {
						if ident, ok := funcDecl.Recv.List[0].Type.(*ast.Ident); ok {
							structName := ident.Name
							// Extract the table name from the return statement
							if funcDecl.Body != nil {
								for _, stmt := range funcDecl.Body.List {
									if ret, ok := stmt.(*ast.ReturnStmt); ok {
										if len(ret.Results) > 0 {
											if lit, ok := ret.Results[0].(*ast.BasicLit); ok {
												tableName := strings.Trim(lit.Value, `"`)
												tableNames[structName] = tableName
											}
										}
									}
								}
							}
						}
					}
				}
			}
			return true
		})
	}

	// Second pass: find foreign key relationships
	// Pattern to match GORM foreignKey tags
	fkPattern := regexp.MustCompile(`foreignKey:(\w+)`)

	for _, file := range files {
		node, err := parser.ParseFile(fset, file, nil, parser.ParseComments)
		if err != nil {
			continue
		}

		ast.Inspect(node, func(n ast.Node) bool {
			typeSpec, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}

			structType, ok := typeSpec.Type.(*ast.StructType)
			if !ok {
				return true
			}

			structName := typeSpec.Name.Name
			if !strings.HasPrefix(structName, "Table") && structName != "SessionsTable" {
				return true
			}

			currentTableName := tableNames[structName]
			if currentTableName == "" {
				return true
			}

			// Check each field for FK relationships
			for _, field := range structType.Fields.List {
				if field.Tag == nil {
					continue
				}

				tag := field.Tag.Value
				
				// Skip fields that are not stored in DB
				if strings.Contains(tag, `gorm:"-"`) {
					continue
				}

				// Look for foreignKey in gorm tag
				fkMatches := fkPattern.FindStringSubmatch(tag)
				if len(fkMatches) < 2 {
					continue
				}
				fkColumn := fkMatches[1]

				// Get field name
				fieldName := ""
				if len(field.Names) > 0 {
					fieldName = field.Names[0].Name
				}

				// Determine the type of relationship based on field type
				var refTableStruct string
				isBelongsTo := false

				switch t := field.Type.(type) {
				case *ast.StarExpr:
					// Pointer type: *TableBudget - this is a "belongs-to" relationship
					// The FK column is on THIS table, referencing the other table
					if ident, ok := t.X.(*ast.Ident); ok {
						refTableStruct = ident.Name
						isBelongsTo = true
					}
				case *ast.ArrayType, *ast.SliceExpr:
					// Slice type: []TableKey - this is a "has-many" relationship
					// The FK column is on the CHILD table, not on this table
					// Parent must be created first, which is natural order
					// We don't need to track this as a dependency
					continue
				case *ast.Ident:
					// Direct type reference
					refTableStruct = t.Name
					isBelongsTo = true
				}

				// Only track belongs-to relationships where THIS table has the FK column
				if !isBelongsTo || refTableStruct == "" || !strings.HasPrefix(refTableStruct, "Table") {
					continue
				}

				// Verify the FK column exists on this struct
				hasFKColumn := false
				for _, f := range structType.Fields.List {
					if len(f.Names) > 0 && f.Names[0].Name == fkColumn {
						hasFKColumn = true
						break
					}
				}

				// If the FK column is on this table, it's a belongs-to relationship
				// The referenced table must be created before this table
				if hasFKColumn {
					refTableName := tableNames[refTableStruct]
					if refTableName != "" {
						dependencies = append(dependencies, TableDependency{
							Table:      currentTableName,
							DependsOn:  refTableName,
							Field:      fieldName,
							SourceFile: filepath.Base(file),
						})
					}
				}
			}
			return true
		})
	}

	return dependencies, nil
}

// parseMigrationOrder parses migrations.go and extracts the order of table creations
func parseMigrationOrder(migrationsPath string) ([]MigrationAction, error) {
	var actions []MigrationAction

	content, err := os.ReadFile(migrationsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read migrations file: %w", err)
	}

	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, migrationsPath, content, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("failed to parse migrations file: %w", err)
	}

	// Track the current migration function being processed
	currentMigration := ""
	order := 0

	// Table struct to table name mapping (simplified)
	tableMapping := map[string]string{
		"TableConfigHash":              "config_hashes",
		"TableBudget":                  "governance_budgets",
		"TableRateLimit":               "governance_rate_limits",
		"TableProvider":                "config_providers",
		"TableKey":                     "config_keys",
		"TableModel":                   "config_models",
		"TableOauthConfig":             "oauth_configs",
		"TableOauthToken":              "oauth_tokens",
		"TableMCPClient":               "config_mcp_clients",
		"TableClientConfig":            "config_client",
		"TableEnvKey":                  "config_env_keys",
		"TableVectorStoreConfig":       "config_vector_stores",
		"TableLogStoreConfig":          "config_log_stores",
		"TableCustomer":                "governance_customers",
		"TableTeam":                    "governance_teams",
		"TableVirtualKey":              "governance_virtual_keys",
		"TableGovernanceConfig":        "governance_configs",
		"TableModelPricing":            "model_pricing",
		"TablePlugin":                  "plugins",
		"TableFrameworkConfig":         "framework_configs",
		"TableVirtualKeyProviderConfig": "governance_virtual_key_provider_configs",
		"TableVirtualKeyMCPConfig":     "governance_virtual_key_mcp_configs",
		"SessionsTable":                "sessions",
		"TableDistributedLock":         "distributed_locks",
		"TableModelConfig":             "model_configs",
		"TableRoutingRule":             "routing_rules",
	}

	ast.Inspect(node, func(n ast.Node) bool {
		// Track function declarations for migration IDs
		if funcDecl, ok := n.(*ast.FuncDecl); ok {
			if strings.HasPrefix(funcDecl.Name.Name, "migration") {
				currentMigration = funcDecl.Name.Name
			}
		}

		// Look for CreateTable calls
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if sel.Sel.Name == "CreateTable" {
					// Extract the table type
					if len(call.Args) > 0 {
						if unary, ok := call.Args[0].(*ast.UnaryExpr); ok {
							if comp, ok := unary.X.(*ast.CompositeLit); ok {
								if sel, ok := comp.Type.(*ast.SelectorExpr); ok {
									structName := sel.Sel.Name
									if tableName, exists := tableMapping[structName]; exists {
										pos := fset.Position(call.Pos())
										actions = append(actions, MigrationAction{
											MigrationID: currentMigration,
											ActionType:  "CreateTable",
											Table:       tableName,
											Order:       order,
											Line:        pos.Line,
										})
										order++
									}
								}
							}
						}
					}
				} else if sel.Sel.Name == "AddColumn" {
					// Extract column additions that might have FK constraints
					if len(call.Args) >= 2 {
						// First arg is table, second is column name
						if unary, ok := call.Args[0].(*ast.UnaryExpr); ok {
							if comp, ok := unary.X.(*ast.CompositeLit); ok {
								if sel, ok := comp.Type.(*ast.SelectorExpr); ok {
									structName := sel.Sel.Name
									if tableName, exists := tableMapping[structName]; exists {
										colName := ""
										if lit, ok := call.Args[1].(*ast.BasicLit); ok {
											colName = strings.Trim(lit.Value, `"`)
										}
										pos := fset.Position(call.Pos())
										actions = append(actions, MigrationAction{
											MigrationID: currentMigration,
											ActionType:  "AddColumn",
											Table:       tableName,
											Column:      colName,
											Order:       order,
											Line:        pos.Line,
										})
										order++
									}
								}
							}
						}
					}
				}
			}
		}
		return true
	})

	return actions, nil
}

// validateDependencies checks if tables with FK dependencies are created after their referenced tables
func validateDependencies(deps []TableDependency, actions []MigrationAction) []string {
	var violations []string

	// Build a map of table -> first creation order
	tableCreationOrder := make(map[string]int)
	tableCreationLine := make(map[string]int)
	tableCreationMigration := make(map[string]string)

	for _, action := range actions {
		if action.ActionType == "CreateTable" {
			if _, exists := tableCreationOrder[action.Table]; !exists {
				tableCreationOrder[action.Table] = action.Order
				tableCreationLine[action.Table] = action.Line
				tableCreationMigration[action.Table] = action.MigrationID
			}
		}
	}

	// Check each dependency
	for _, dep := range deps {
		depOrder, depExists := tableCreationOrder[dep.Table]
		refOrder, refExists := tableCreationOrder[dep.DependsOn]

		// Skip if either table isn't created via migrations (might be handled elsewhere)
		if !depExists || !refExists {
			continue
		}

		// The table with the FK (dep.Table) should be created AFTER the referenced table (dep.DependsOn)
		// Violation: dep.Table is created before dep.DependsOn
		if depOrder < refOrder {
			violations = append(violations, fmt.Sprintf(
				"  - Table '%s' (line %d, %s) is created before '%s' (line %d, %s)\n"+
					"    but '%s' has a FK column referencing '%s' via field '%s' (defined in %s)",
				dep.Table, tableCreationLine[dep.Table], tableCreationMigration[dep.Table],
				dep.DependsOn, tableCreationLine[dep.DependsOn], tableCreationMigration[dep.DependsOn],
				dep.Table, dep.DependsOn, dep.Field, dep.SourceFile,
			))
		}
	}

	// Sort violations for consistent output
	sort.Strings(violations)

	return violations
}
