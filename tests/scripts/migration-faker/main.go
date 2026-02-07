// Package main provides a dynamic faker data generator for migration tests.
// It introspects the database schema at runtime and generates appropriate
// fake data for all tables, respecting foreign key dependencies.
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"os"
	"strings"

	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"
)

func main() {
	// Command line flags
	dbType := flag.String("db-type", "postgres", "Database type: postgres or sqlite")
	host := flag.String("host", "localhost", "Database host (postgres only)")
	port := flag.Int("port", 5432, "Database port (postgres only)")
	user := flag.String("user", "bifrost", "Database user (postgres only)")
	password := flag.String("password", "bifrost_password", "Database password (postgres only)")
	database := flag.String("database", "bifrost", "Database name (postgres only)")
	sslMode := flag.String("sslmode", "disable", "SSL mode (postgres only)")
	sqlitePath := flag.String("sqlite-path", "", "Path to SQLite database file")
	output := flag.String("output", "", "Output file path (stdout if empty)")
	rowsPerTable := flag.Int("rows", 2, "Default number of rows per table")
	execute := flag.Bool("execute", false, "Execute the generated SQL directly")
	verbose := flag.Bool("verbose", false, "Verbose output")

	flag.Parse()

	// Validate inputs
	if *dbType != "postgres" && *dbType != "sqlite" {
		fmt.Fprintf(os.Stderr, "Error: db-type must be 'postgres' or 'sqlite'\n")
		os.Exit(1)
	}

	if *dbType == "sqlite" && *sqlitePath == "" {
		fmt.Fprintf(os.Stderr, "Error: sqlite-path is required for sqlite db-type\n")
		os.Exit(1)
	}

	// Connect to database
	db, err := connectDB(*dbType, *host, *port, *user, *password, *database, *sslMode, *sqlitePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting to database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	if *verbose {
		fmt.Fprintf(os.Stderr, "Connected to %s database\n", *dbType)
	}

	// Create schema introspector
	var introspector SchemaIntrospector
	if *dbType == "postgres" {
		introspector = NewPostgresIntrospector(db)
	} else {
		introspector = NewSQLiteIntrospector(db)
	}

	// Load schema
	tables, err := LoadSchema(introspector, SkipTables)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading schema: %v\n", err)
		os.Exit(1)
	}

	if *verbose {
		fmt.Fprintf(os.Stderr, "Found %d tables\n", len(tables))
	}

	// Get insert order via topological sort
	insertOrder, err := GetInsertOrder(tables)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error determining insert order: %v\n", err)
		os.Exit(1)
	}

	if *verbose {
		fmt.Fprintf(os.Stderr, "Insert order: %s\n", strings.Join(insertOrder, " -> "))
	}

	// Generate SQL
	sql, err := GenerateAllInsertsEnhanced(tables, insertOrder, *dbType, SpecialColumns, *rowsPerTable)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error generating SQL: %v\n", err)
		os.Exit(1)
	}

	// Output or execute
	if *execute {
		if *verbose {
			fmt.Fprintf(os.Stderr, "Executing generated SQL...\n")
		}
		if err := executeSQL(db, sql); err != nil {
			fmt.Fprintf(os.Stderr, "Error executing SQL: %v\n", err)
			os.Exit(1)
		}
		if *verbose {
			fmt.Fprintf(os.Stderr, "SQL executed successfully\n")
		}
	} else if *output != "" {
		if err := os.WriteFile(*output, []byte(sql), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing output file: %v\n", err)
			os.Exit(1)
		}
		if *verbose {
			fmt.Fprintf(os.Stderr, "SQL written to %s\n", *output)
		}
	} else {
		fmt.Print(sql)
	}
}

func connectDB(dbType, host string, port int, user, password, database, sslMode, sqlitePath string) (*sql.DB, error) {
	var dsn string
	var driver string

	if dbType == "postgres" {
		driver = "postgres"
		dsn = fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
			host, port, user, password, database, sslMode)
	} else {
		driver = "sqlite3"
		dsn = sqlitePath
	}

	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Test connection
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	return db, nil
}

func executeSQL(db *sql.DB, sqlContent string) error {
	// Split by semicolons and execute each statement
	statements := strings.Split(sqlContent, ";")
	for _, stmt := range statements {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" || strings.HasPrefix(stmt, "--") {
			continue
		}
		if _, err := db.Exec(stmt); err != nil {
			// Log but continue on conflict errors
			if strings.Contains(err.Error(), "duplicate") ||
				strings.Contains(err.Error(), "UNIQUE constraint") ||
				strings.Contains(err.Error(), "already exists") {
				continue
			}
			return fmt.Errorf("failed to execute statement: %s\nError: %w", stmt[:min(100, len(stmt))], err)
		}
	}
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// GenerateAllInsertsEnhanced generates INSERT statements with enhanced handling
func GenerateAllInsertsEnhanced(tables []Table, insertOrder []string, dbType string, specialColumns map[string]map[string]string, defaultRows int) (string, error) {
	generator := NewValueGenerator(dbType, specialColumns)

	var allStatements []string
	allStatements = append(allStatements, "-- Auto-generated faker data for migration tests")
	allStatements = append(allStatements, fmt.Sprintf("-- Database type: %s", dbType))
	allStatements = append(allStatements, fmt.Sprintf("-- Tables: %d", len(insertOrder)))
	allStatements = append(allStatements, "")

	for _, tableName := range insertOrder {
		table := GetTableByName(tables, tableName)
		if table == nil {
			continue
		}

		// Get row count for this table
		rowCount := GetRowsForTable(tableName, defaultRows)

		fkMap := GetForeignKeyMap(table)

		allStatements = append(allStatements, fmt.Sprintf("-- Table: %s (%d rows)", tableName, rowCount))
		stmts := generateEnhancedInserts(generator, table, fkMap, rowCount, dbType)
		allStatements = append(allStatements, stmts...)
		allStatements = append(allStatements, "")
	}

	return strings.Join(allStatements, "\n"), nil
}

func generateEnhancedInserts(generator *ValueGenerator, table *Table, fkMap map[string]ForeignKeyInfo, numRows int, dbType string) []string {
	var statements []string

	for row := 0; row < numRows; row++ {
		var columns []string
		var values []string
		var pkColumn string
		var pkValue string

		for _, col := range table.Columns {
			// Skip auto-increment columns
			if col.IsAutoInc {
				continue
			}

			// Skip columns that should be NULL (nullable JSON columns that trigger validation)
			if col.IsNullable && ShouldBeNull(table.Name, col.Name) {
				continue
			}

			// Get FK info if exists
			var fkInfo *ForeignKeyInfo
			if info, ok := fkMap[col.Name]; ok {
				fkInfo = &info
			}

			// Check for special value first
			var value string
			if specialVal, ok := GetSpecialValue(table.Name, col.Name); ok {
				// Check if the special value is for a boolean column - don't quote
				dataType := strings.ToLower(col.DataType)
				if dataType == "boolean" || dataType == "bool" {
					// Boolean values shouldn't be quoted
					if dbType == "postgres" {
						value = specialVal // "true" or "false"
					} else {
						// SQLite uses 1/0
						if specialVal == "true" {
							value = "1"
						} else {
							value = "0"
						}
					}
				} else {
					value = quoteString(specialVal)
				}
			} else if fkInfo != nil {
				// Handle FK reference
				value = generator.getForeignKeyValue(fkInfo)
			} else {
				// Check for JSON array columns
				dataType := strings.ToLower(col.DataType)
				if dataType == "json" || dataType == "jsonb" || dataType == "text" {
					if isJSONColumn(col.Name) {
						value = quoteString(GetJSONDefault(table.Name, col.Name))
					} else {
						value = generator.generateByType(table.Name, col)
					}
				} else if dataType == "boolean" || dataType == "bool" {
					boolVal := generator.generateBoolean(col)
					if dbType == "postgres" {
						value = fmt.Sprintf("%t", boolVal)
					} else {
						if boolVal {
							value = "1"
						} else {
							value = "0"
						}
					}
				} else {
					value = generator.generateByType(table.Name, col)
				}
			}

			// Skip NULL for non-nullable columns without FK
			if value == "NULL" && !col.IsNullable {
				// Use a default value based on type
				value = getDefaultForType(col, dbType)
			}

			// Skip empty values
			if value == "" {
				continue
			}

			columns = append(columns, col.Name)
			values = append(values, value)

			// Track primary key for FK references
			if col.IsPrimaryKey && !col.IsAutoInc {
				pkColumn = col.Name
				pkValue = value
			}
		}

		if len(columns) == 0 {
			continue
		}

		// Build INSERT statement
		var stmt string
		if dbType == "postgres" {
			stmt = fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) ON CONFLICT DO NOTHING;",
				table.Name,
				strings.Join(columns, ", "),
				strings.Join(values, ", "))
		} else {
			stmt = fmt.Sprintf("INSERT OR IGNORE INTO %s (%s) VALUES (%s);",
				table.Name,
				strings.Join(columns, ", "),
				strings.Join(values, ", "))
		}

		statements = append(statements, stmt)

		// Record PK for FK references
		if pkColumn != "" && pkValue != "" {
			generator.RecordGeneratedPK(table.Name, pkValue)
		}
	}

	return statements
}

func isJSONColumn(colName string) bool {
	lowerName := strings.ToLower(colName)
	// Check for common JSON column suffixes
	jsonSuffixes := []string{"_json", "_config", "_settings"}
	for _, suffix := range jsonSuffixes {
		if strings.HasSuffix(lowerName, suffix) {
			return true
		}
	}
	// Also check if name contains "json" anywhere
	if strings.Contains(lowerName, "json") {
		return true
	}
	return false
}

func getDefaultForType(col Column, dbType string) string {
	dataType := strings.ToLower(col.DataType)

	switch {
	case strings.Contains(dataType, "varchar") || strings.Contains(dataType, "char") || dataType == "text":
		return quoteString("default")
	case strings.Contains(dataType, "int"):
		return "0"
	case strings.Contains(dataType, "float") || strings.Contains(dataType, "double") ||
		strings.Contains(dataType, "numeric") || strings.Contains(dataType, "decimal"):
		return "0.0"
	case dataType == "boolean" || dataType == "bool":
		if dbType == "postgres" {
			return "false"
		}
		return "0"
	case strings.Contains(dataType, "timestamp"):
		if dbType == "postgres" {
			return "NOW()"
		}
		return "datetime('now')"
	case dataType == "json" || dataType == "jsonb":
		return quoteString("{}")
	default:
		return quoteString("default")
	}
}

func quoteString(s string) string {
	escaped := strings.ReplaceAll(s, "'", "''")
	return fmt.Sprintf("'%s'", escaped)
}
