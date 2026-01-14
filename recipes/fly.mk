# Fly.io Deployment Recipe
# Include this in your main Makefile with: include recipes/fly.mk

.PHONY: deploy-to-fly-io

deploy-to-fly-io: ## Deploy to Fly.io (Usage: make deploy-to-fly-io APP_NAME=your-app-name)
	@echo "$(BLUE)Starting Fly.io deployment...$(NC)"
	@echo ""
	@# Check if APP_NAME is provided
	@if [ -z "$(APP_NAME)" ]; then \
		echo "$(RED)Error: APP_NAME is required$(NC)"; \
		echo "$(YELLOW)Usage: make deploy-to-fly-io APP_NAME=your-app-name$(NC)"; \
		exit 1; \
	fi
	@echo "$(YELLOW)Checking prerequisites...$(NC)"
	@# Check if docker is installed
	@which docker > /dev/null || (echo "$(RED)Error: Docker is not installed. Please install Docker first.$(NC)" && exit 1)
	@echo "$(GREEN)✓ Docker is installed$(NC)"
	@# Check if flyctl is installed
	@which flyctl > /dev/null || (echo "$(RED)Error: flyctl is not installed. Please install flyctl first.$(NC)" && exit 1)
	@echo "$(GREEN)✓ flyctl is installed$(NC)"
	@# Check if app exists on Fly.io
	@flyctl status -a $(APP_NAME) > /dev/null 2>&1 || (echo "$(RED)Error: App '$(APP_NAME)' not found on Fly.io$(NC)" && echo "$(YELLOW)Create the app first with: flyctl launch --name $(APP_NAME)$(NC)" && exit 1)
	@echo "$(GREEN)✓ App '$(APP_NAME)' exists on Fly.io$(NC)"
	@echo ""
	@# Check if fly.toml exists, create temp if needed
	@if [ -f "fly.toml" ]; then \
		echo "$(GREEN)✓ Using existing fly.toml$(NC)"; \
	else \
		echo "$(YELLOW)fly.toml not found in current directory$(NC)"; \
		echo "$(CYAN)Would you like to create a temporary fly.toml with 2 vCPU configuration?$(NC)"; \
		echo "$(CYAN)(It will be removed after deployment)$(NC)"; \
		printf "Create temporary fly.toml? [y/N]: "; read response; \
		case "$$response" in \
			[yY][eE][sS]|[yY]) \
				echo "$(YELLOW)Creating temporary fly.toml with 2 vCPU configuration...$(NC)"; \
				echo "app = '$(APP_NAME)'" > fly.toml; \
				echo "primary_region = 'iad'" >> fly.toml; \
				echo "" >> fly.toml; \
				echo "[build]" >> fly.toml; \
				echo "  image = 'registry.fly.io/$(APP_NAME):latest'" >> fly.toml; \
				echo "" >> fly.toml; \
				echo "[http_service]" >> fly.toml; \
				echo "  internal_port = 8080" >> fly.toml; \
				echo "  force_https = true" >> fly.toml; \
				echo "  auto_stop_machines = true" >> fly.toml; \
				echo "  auto_start_machines = true" >> fly.toml; \
				echo "  min_machines_running = 0" >> fly.toml; \
				echo "" >> fly.toml; \
				echo "[[vm]]" >> fly.toml; \
				echo "  memory = '2gb'" >> fly.toml; \
				echo "  cpu_kind = 'shared'" >> fly.toml; \
				echo "  cpus = 2" >> fly.toml; \
				echo "$(GREEN)✓ Created temporary fly.toml with 2 vCPU configuration$(NC)"; \
				touch .fly.toml.tmp.marker; \
				;; \
			*) \
				echo "$(RED)Deployment cancelled. Please create a fly.toml file or run 'flyctl launch' first.$(NC)"; \
				exit 1; \
				;; \
		esac; \
	fi
	@echo ""
	@echo "$(YELLOW)Building Docker image...$(NC)"
	@$(MAKE) build-docker-image
	@echo ""
	@echo "$(YELLOW)Tagging image for Fly.io registry...$(NC)"
	@docker tag bifrost:latest registry.fly.io/$(APP_NAME):latest
	$(eval GIT_SHA=$(shell git rev-parse --short HEAD))
	@docker tag bifrost:$(GIT_SHA) registry.fly.io/$(APP_NAME):$(GIT_SHA)
	@echo "$(GREEN)✓ Tagged: registry.fly.io/$(APP_NAME):latest$(NC)"
	@echo "$(GREEN)✓ Tagged: registry.fly.io/$(APP_NAME):$(GIT_SHA)$(NC)"
	@echo ""
	@echo "$(YELLOW)Pushing to Fly.io registry...$(NC)"
	@echo "$(YELLOW)Authenticating with Fly.io...$(NC)"
	@flyctl auth docker
	@echo "$(GREEN)✓ Authenticated with Fly.io$(NC)"
	@echo ""
	@echo "$(YELLOW)Pushing image to Fly.io registry...$(NC)"
	@docker push registry.fly.io/$(APP_NAME):latest
	@docker push registry.fly.io/$(APP_NAME):$(GIT_SHA)
	@echo "$(GREEN)✓ Image pushed to registry$(NC)"
	@echo ""
	@echo "$(YELLOW)Deploying to Fly.io...$(NC)"
	@flyctl deploy -a $(APP_NAME)
	@echo ""
	@echo "$(GREEN)✓ Deployment complete!$(NC)"
	@echo "$(CYAN)App URL: https://$(APP_NAME).fly.dev$(NC)"
	@echo ""
	@# Clean up temporary fly.toml if we created it
	@if [ -f ".fly.toml.tmp.marker" ]; then \
		echo "$(YELLOW)Cleaning up temporary fly.toml...$(NC)"; \
		rm -f fly.toml .fly.toml.tmp.marker; \
		echo "$(GREEN)✓ Temporary fly.toml removed$(NC)"; \
	fi

