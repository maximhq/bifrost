.PHONY: help populate-sqlite populate-postgres populate-small populate-large clean

help:
	@echo "Bifrost Test Scripts"
	@echo "===================="
	@echo ""
	@echo "Available targets:"
	@echo "  populate-sqlite    - Populate SQLite with 1M rows (~17.5 GB)"
	@echo "  populate-postgres  - Populate PostgreSQL with 1M rows (~17.5 GB)"
	@echo "  populate-small     - Quick test with 10K rows (~175 MB)"
	@echo "  populate-large     - Large dataset with 2M rows (~35 GB)"
	@echo "  clean             - Remove generated SQLite database"
	@echo ""
	@echo "Examples:"
	@echo "  make populate-sqlite"
	@echo "  make populate-postgres POSTGRES_PASSWORD=yourpassword"
	@echo ""

# Default values
ROWS ?= 1000000
SIZE ?= 17.5
BATCH ?= 1000
DB_PATH ?= ../../tests/configs/default/logs.db

POSTGRES_HOST ?= localhost
POSTGRES_PORT ?= 5432
POSTGRES_USER ?= postgres
POSTGRES_PASSWORD ?= 
POSTGRES_DB ?= bifrost_logs

# Populate SQLite database (default)
populate-sqlite:
	@echo "🚀 Populating SQLite database..."
	@echo "   Rows: $(ROWS)"
	@echo "   Target size: $(SIZE) GB"
	@echo "   Database: $(DB_PATH)"
	@echo ""
	go run main.go \
		-db sqlite \
		-path $(DB_PATH) \
		-rows $(ROWS) \
		-size $(SIZE) \
		-batch 50

# Populate PostgreSQL database
populate-postgres:
	@echo "🚀 Populating PostgreSQL database..."
	@echo "   Rows: $(ROWS)"
	@echo "   Target size: $(SIZE) GB"
	@echo "   Host: $(POSTGRES_HOST):$(POSTGRES_PORT)"
	@echo "   Database: $(POSTGRES_DB)"
	@echo ""
	go run main.go \
		-db postgres \
		-host $(POSTGRES_HOST) \
		-port $(POSTGRES_PORT) \
		-user $(POSTGRES_USER) \
		-password $(POSTGRES_PASSWORD) \
		-dbname $(POSTGRES_DB) \
		-rows $(ROWS) \
		-size $(SIZE) \
		-batch $(BATCH)

# Quick test with small dataset
populate-small:
	@echo "🚀 Creating small test dataset..."
	@$(MAKE) populate-sqlite ROWS=10000 SIZE=0.175

# Large dataset for stress testing
populate-large:
	@echo "🚀 Creating large test dataset..."
	@$(MAKE) populate-sqlite ROWS=2000000 SIZE=35

# Custom parameters
populate-custom:
	@if [ -z "$(ROWS)" ] || [ -z "$(SIZE)" ]; then \
		echo "❌ Error: ROWS and SIZE parameters are required"; \
		echo "Usage: make populate-custom ROWS=500000 SIZE=8.75"; \
		exit 1; \
	fi
	@echo "🚀 Creating custom dataset..."
	@$(MAKE) populate-sqlite ROWS=$(ROWS) SIZE=$(SIZE)

# Clean up generated files
clean:
	@echo "🧹 Cleaning up..."
	rm -f $(DB_PATH)
	rm -f $(DB_PATH)-shm
	rm -f $(DB_PATH)-wal
	@echo "✅ Done!"

# Get database stats
stats:
	@if [ -f "$(DB_PATH)" ]; then \
		echo "📊 Database Statistics"; \
		echo "======================"; \
		du -h $(DB_PATH) | awk '{print "Size: " $$1}'; \
		sqlite3 $(DB_PATH) "SELECT COUNT(*) FROM logs" | awk '{print "Rows: " $$1}'; \
	else \
		echo "❌ Database not found at: $(DB_PATH)"; \
	fi

