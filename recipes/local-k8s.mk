# Local Kubernetes Deployment Recipe
# Include this in your main Makefile with: include recipes/local-k8s.mk

# Configuration variables
K8S_NAMESPACE ?= bifrost
K8S_RELEASE_NAME ?= bifrost
K8S_HOST ?= bifrost.local
BIFROST_IMAGE_TAG ?= v1.3.38

# Storage configuration (set via prompts or override)
# Options: no, sqlite, postgres
K8S_CONFIG_STORE ?=
K8S_LOG_STORE ?=

# Spinner characters for loading animation
SPINNER = ⠋ ⠙ ⠹ ⠸ ⠼ ⠴ ⠦ ⠧ ⠇ ⠏

# Helper function to run command with spinner
# Usage: $(call run_with_spinner,command,message)
define run_with_spinner
	@( \
		$(1) > /tmp/k8s-cmd-output.log 2>&1 & \
		CMD_PID=$$!; \
		SPINNER_CHARS="⠋ ⠙ ⠹ ⠸ ⠼ ⠴ ⠦ ⠧ ⠇ ⠏"; \
		i=0; \
		while kill -0 $$CMD_PID 2>/dev/null; do \
			CHAR=$$(echo $$SPINNER_CHARS | cut -d' ' -f$$((i % 10 + 1))); \
			printf "\r  $(CYAN)$$CHAR $(2)$(NC)"; \
			i=$$((i + 1)); \
			sleep 0.1; \
		done; \
		wait $$CMD_PID; \
		EXIT_CODE=$$?; \
		printf "\r"; \
		if [ $$EXIT_CODE -eq 0 ]; then \
			echo "$(GREEN)✓ $(2) - done$(NC)"; \
		else \
			echo "$(RED)✗ $(2) - failed$(NC)"; \
			echo "$(YELLOW)Error output:$(NC)"; \
			cat /tmp/k8s-cmd-output.log; \
			exit 1; \
		fi \
	)
endef

.PHONY: deploy-local-k8s setup-local-k8s-prerequisites setup-local-k8s-ingress setup-local-k8s-tls setup-local-k8s-secrets deploy-local-k8s-helm local-k8s-update-key local-k8s-status local-k8s-logs local-k8s-port-forward cleanup-local-k8s

deploy-local-k8s: ## Deploy Bifrost to local Kubernetes with storage options + HTTPS (Usage: make deploy-local-k8s [OPENAI_API_KEY='...'])
	@echo ""
	@echo "$(BLUE)╔════════════════════════════════════════════════════════════════╗$(NC)"
	@echo "$(BLUE)║  Bifrost Local K8s - Configurable Storage + Secrets + HTTPS    ║$(NC)"
	@echo "$(BLUE)╚════════════════════════════════════════════════════════════════╝$(NC)"
	@echo ""
	@# Prompt for storage options
	@echo "$(CYAN)Storage Configuration$(NC)"
	@echo ""
	@echo "$(YELLOW)Config Store:$(NC)"
	@echo "  1) no (disabled)"
	@echo "  2) sqlite"
	@echo "  3) postgres"
	@printf "  Select [2]: "; \
	read CONFIG_CHOICE; \
	case "$$CONFIG_CHOICE" in \
		1) echo "no" > /tmp/bifrost-config-store.tmp ;; \
		3) echo "postgres" > /tmp/bifrost-config-store.tmp ;; \
		*) echo "sqlite" > /tmp/bifrost-config-store.tmp ;; \
	esac
	@echo ""
	@echo "$(YELLOW)Log Store:$(NC)"
	@echo "  1) no (disabled)"
	@echo "  2) sqlite"
	@echo "  3) postgres"
	@printf "  Select [2]: "; \
	read LOG_CHOICE; \
	case "$$LOG_CHOICE" in \
		1) echo "no" > /tmp/bifrost-log-store.tmp ;; \
		3) echo "postgres" > /tmp/bifrost-log-store.tmp ;; \
		*) echo "sqlite" > /tmp/bifrost-log-store.tmp ;; \
	esac
	@echo ""
	@echo "$(CYAN)Step 1/5: Checking prerequisites$(NC)"
	@$(MAKE) setup-local-k8s-prerequisites
	@echo "$(CYAN)Step 2/5: Setting up ingress controller$(NC)"
	@$(MAKE) setup-local-k8s-ingress
	@echo "$(CYAN)Step 3/5: Generating TLS certificate$(NC)"
	@$(MAKE) setup-local-k8s-tls
	@echo "$(CYAN)Step 4/5: Creating secrets$(NC)"
	@$(MAKE) setup-local-k8s-secrets
	@echo "$(CYAN)Step 5/5: Deploying Bifrost$(NC)"
	@CONFIG_STORE=$$(cat /tmp/bifrost-config-store.tmp 2>/dev/null || echo "sqlite"); \
	LOG_STORE=$$(cat /tmp/bifrost-log-store.tmp 2>/dev/null || echo "sqlite"); \
	$(MAKE) deploy-local-k8s-helm K8S_CONFIG_STORE="$$CONFIG_STORE" K8S_LOG_STORE="$$LOG_STORE" K8S_SKIP_PROMPTS=true
	@rm -f /tmp/bifrost-config-store.tmp /tmp/bifrost-log-store.tmp
	@echo ""
	@echo "$(GREEN)╔════════════════════════════════════════════════════════════════╗$(NC)"
	@echo "$(GREEN)║                    DEPLOYMENT COMPLETE!                        ║$(NC)"
	@echo "$(GREEN)╚════════════════════════════════════════════════════════════════╝$(NC)"
	@echo ""
	@INGRESS_IP=$$(kubectl get svc -n ingress-nginx ingress-nginx-controller -o jsonpath='{.status.loadBalancer.ingress[0].ip}' 2>/dev/null || echo "127.0.0.1"); \
	echo "$(CYAN)Next Steps:$(NC)"; \
	echo "  1. Add to /etc/hosts: $(YELLOW)echo '$$INGRESS_IP $(K8S_HOST)' | sudo tee -a /etc/hosts$(NC)"; \
	echo "  2. Access: $(YELLOW)https://$(K8S_HOST)$(NC)"; \
	echo "  3. Or port-forward: $(YELLOW)make local-k8s-port-forward$(NC)"; \
	echo ""

setup-local-k8s-prerequisites: ## Check local K8s prerequisites
	@which kubectl > /dev/null || (echo "$(RED)✗ kubectl not installed$(NC)" && exit 1)
	@echo "  $(GREEN)✓ kubectl$(NC)"
	@which helm > /dev/null || (echo "$(RED)✗ helm not installed$(NC)" && exit 1)
	@echo "  $(GREEN)✓ helm$(NC)"
	@which openssl > /dev/null || (echo "$(RED)✗ openssl not installed$(NC)" && exit 1)
	@echo "  $(GREEN)✓ openssl$(NC)"
	@kubectl cluster-info > /dev/null 2>&1 || (echo "$(RED)✗ Cannot connect to K8s cluster$(NC)" && exit 1)
	@echo "  $(GREEN)✓ Kubernetes cluster$(NC)"
	@kubectl create namespace $(K8S_NAMESPACE) --dry-run=client -o yaml | kubectl apply -f - >/dev/null 2>&1
	@echo "  $(GREEN)✓ Namespace '$(K8S_NAMESPACE)'$(NC)"
	@echo ""

setup-local-k8s-ingress: ## Install nginx-ingress controller
	@if kubectl get pods -n ingress-nginx -l app.kubernetes.io/name=ingress-nginx 2>/dev/null | grep -q Running; then \
		echo "  $(GREEN)✓ nginx-ingress already running$(NC)"; \
	else \
		echo "  $(YELLOW)Installing nginx-ingress (this may take 2-3 minutes)...$(NC)"; \
		( \
			helm repo add ingress-nginx https://kubernetes.github.io/ingress-nginx >/dev/null 2>&1 || true; \
			helm repo update >/dev/null 2>&1; \
			helm upgrade --install ingress-nginx ingress-nginx/ingress-nginx \
				--namespace ingress-nginx \
				--create-namespace \
				--set controller.service.type=LoadBalancer \
				--wait --timeout 300s >/dev/null 2>&1 & \
			CMD_PID=$$!; \
			ELAPSED=0; \
			while kill -0 $$CMD_PID 2>/dev/null; do \
				PODS_READY=$$(kubectl get pods -n ingress-nginx -l app.kubernetes.io/name=ingress-nginx -o jsonpath='{.items[*].status.phase}' 2>/dev/null | grep -c Running || echo 0); \
				SPINNER_CHARS="⠋ ⠙ ⠹ ⠸ ⠼ ⠴ ⠦ ⠧ ⠇ ⠏"; \
				CHAR=$$(echo $$SPINNER_CHARS | cut -d' ' -f$$((ELAPSED % 10 + 1))); \
				printf "\r  $(CYAN)$$CHAR Installing nginx-ingress... [%ds] pods ready: %s$(NC)  " "$$ELAPSED" "$$PODS_READY"; \
				ELAPSED=$$((ELAPSED + 1)); \
				sleep 1; \
			done; \
			wait $$CMD_PID; \
			EXIT_CODE=$$?; \
			printf "\r%-70s\r" " "; \
			if [ $$EXIT_CODE -eq 0 ]; then \
				echo "  $(GREEN)✓ nginx-ingress installed [$$ELAPSED seconds]$(NC)"; \
			else \
				echo "  $(RED)✗ nginx-ingress installation failed$(NC)"; \
				echo "  $(YELLOW)Run 'kubectl get pods -n ingress-nginx' to debug$(NC)"; \
				exit 1; \
			fi \
		); \
	fi
	@echo ""

setup-local-k8s-tls: ## Generate self-signed TLS certificate
	@mkdir -p /tmp/bifrost-certs
	@openssl genrsa -out /tmp/bifrost-certs/tls.key 2048 2>/dev/null
	@openssl req -new -x509 -key /tmp/bifrost-certs/tls.key -out /tmp/bifrost-certs/tls.crt \
		-days 365 -subj "/CN=$(K8S_HOST)" \
		-addext "subjectAltName=DNS:$(K8S_HOST),DNS:localhost,IP:127.0.0.1" 2>/dev/null
	@kubectl create secret tls bifrost-tls-secret \
		--cert=/tmp/bifrost-certs/tls.crt --key=/tmp/bifrost-certs/tls.key \
		-n $(K8S_NAMESPACE) --dry-run=client -o yaml | kubectl apply -f - >/dev/null 2>&1
	@rm -rf /tmp/bifrost-certs
	@echo "  $(GREEN)✓ TLS certificate for $(K8S_HOST)$(NC)"
	@echo ""

setup-local-k8s-secrets: ## Create Kubernetes secrets for Bifrost
	@# Ensure namespace exists first
	@kubectl create namespace $(K8S_NAMESPACE) --dry-run=client -o yaml 2>/dev/null | kubectl apply -f - >/dev/null 2>&1 || true
	$(eval BIFROST_ENCRYPTION_KEY ?= $(shell openssl rand -base64 32))
	$(eval OPENAI_KEY := $(if $(OPENAI_API_KEY),$(OPENAI_API_KEY),sk-placeholder-replace-me))
	@kubectl create secret generic bifrost-provider-keys \
		--from-literal=openai-api-key='$(OPENAI_KEY)' \
		-n $(K8S_NAMESPACE) --dry-run=client -o yaml | kubectl apply -f -
	@echo "  $(GREEN)✓ bifrost-provider-keys$(NC)"
	@kubectl create secret generic bifrost-encryption-key \
		--from-literal=encryption-key='$(BIFROST_ENCRYPTION_KEY)' \
		-n $(K8S_NAMESPACE) --dry-run=client -o yaml | kubectl apply -f -
	@echo "  $(GREEN)✓ bifrost-encryption-key$(NC)"
	@if [ "$(OPENAI_KEY)" = "sk-placeholder-replace-me" ]; then \
		echo "  $(YELLOW)⚠ Using placeholder OpenAI key$(NC)"; \
		echo "    Update later: make local-k8s-update-key OPENAI_API_KEY='sk-...'"; \
	fi
	@echo ""

deploy-local-k8s-helm: ## Deploy Bifrost using Helm (Usage: make deploy-local-k8s-helm [K8S_CONFIG_STORE=sqlite] [K8S_LOG_STORE=postgres] [BIFROST_IMAGE_TAG=v1.3.38])
	@# Show prompts if not called from deploy-local-k8s (K8S_SKIP_PROMPTS not set)
	@if [ -z "$(K8S_SKIP_PROMPTS)" ]; then \
		echo ""; \
		echo "$(BLUE)╔════════════════════════════════════════════════════════════════╗$(NC)"; \
		echo "$(BLUE)║  Bifrost Helm Deployment                                       ║$(NC)"; \
		echo "$(BLUE)╚════════════════════════════════════════════════════════════════╝$(NC)"; \
		echo ""; \
		echo "$(CYAN)Deployment Configuration$(NC)"; \
		echo ""; \
		echo "$(YELLOW)Image Tag$(NC) [$(BIFROST_IMAGE_TAG)]:"; \
		printf "  Enter tag: "; \
		read IMAGE_TAG_INPUT; \
		if [ -n "$$IMAGE_TAG_INPUT" ]; then \
			echo "$$IMAGE_TAG_INPUT" > /tmp/bifrost-image-tag.tmp; \
		else \
			echo "$(BIFROST_IMAGE_TAG)" > /tmp/bifrost-image-tag.tmp; \
		fi; \
		echo ""; \
		echo "$(YELLOW)Helm Chart Version$(NC) (leave empty for local chart):"; \
		printf "  Enter version: "; \
		read HELM_VERSION_INPUT; \
		echo "$$HELM_VERSION_INPUT" > /tmp/bifrost-helm-version.tmp; \
		echo ""; \
		echo "$(YELLOW)Config Store:$(NC)"; \
		echo "  1) no (disabled)"; \
		echo "  2) sqlite"; \
		echo "  3) postgres"; \
		printf "  Select [2]: "; \
		read CONFIG_CHOICE; \
		case "$$CONFIG_CHOICE" in \
			1) echo "no" > /tmp/bifrost-config-store.tmp ;; \
			3) echo "postgres" > /tmp/bifrost-config-store.tmp ;; \
			*) echo "sqlite" > /tmp/bifrost-config-store.tmp ;; \
		esac; \
		echo ""; \
		echo "$(YELLOW)Log Store:$(NC)"; \
		echo "  1) no (disabled)"; \
		echo "  2) sqlite"; \
		echo "  3) postgres"; \
		printf "  Select [2]: "; \
		read LOG_CHOICE; \
		case "$$LOG_CHOICE" in \
			1) echo "no" > /tmp/bifrost-log-store.tmp ;; \
			3) echo "postgres" > /tmp/bifrost-log-store.tmp ;; \
			*) echo "sqlite" > /tmp/bifrost-log-store.tmp ;; \
		esac; \
		echo ""; \
	fi
	@# Read configuration from temp files or use provided values
	@CONFIG_STORE="$(K8S_CONFIG_STORE)"; \
	if [ -z "$$CONFIG_STORE" ] && [ -f /tmp/bifrost-config-store.tmp ]; then \
		CONFIG_STORE=$$(cat /tmp/bifrost-config-store.tmp); \
	fi; \
	if [ -z "$$CONFIG_STORE" ]; then \
		CONFIG_STORE="sqlite"; \
	fi; \
	LOG_STORE="$(K8S_LOG_STORE)"; \
	if [ -z "$$LOG_STORE" ] && [ -f /tmp/bifrost-log-store.tmp ]; then \
		LOG_STORE=$$(cat /tmp/bifrost-log-store.tmp); \
	fi; \
	if [ -z "$$LOG_STORE" ]; then \
		LOG_STORE="sqlite"; \
	fi; \
	IMAGE_TAG="$(BIFROST_IMAGE_TAG)"; \
	if [ -f /tmp/bifrost-image-tag.tmp ]; then \
		IMAGE_TAG=$$(cat /tmp/bifrost-image-tag.tmp); \
	fi; \
	HELM_VERSION=""; \
	if [ -f /tmp/bifrost-helm-version.tmp ]; then \
		HELM_VERSION=$$(cat /tmp/bifrost-helm-version.tmp); \
	fi; \
	echo "  $(CYAN)Configuration:$(NC)"; \
	echo "    Image Tag: $$IMAGE_TAG"; \
	if [ -n "$$HELM_VERSION" ]; then \
		echo "    Chart: bifrost/bifrost v$$HELM_VERSION (remote)"; \
	else \
		echo "    Chart: ./helm-charts/bifrost (local)"; \
	fi; \
	echo "    Config Store: $$CONFIG_STORE"; \
	echo "    Log Store: $$LOG_STORE"; \
	echo ""; \
	echo "  $(YELLOW)Deploying Bifrost $$IMAGE_TAG (this may take 1-2 minutes)...$(NC)"; \
	NEEDS_POSTGRES="false"; \
	if [ "$$CONFIG_STORE" = "postgres" ] || [ "$$LOG_STORE" = "postgres" ]; then \
		NEEDS_POSTGRES="true"; \
	fi; \
	POSTGRES_PASSWORD=""; \
	if [ "$$NEEDS_POSTGRES" = "true" ]; then \
		EXISTING_PG_PWD=$$(kubectl get secret $(K8S_RELEASE_NAME)-postgresql -n $(K8S_NAMESPACE) -o jsonpath='{.data.password}' 2>/dev/null | base64 -d 2>/dev/null || true); \
		if [ -n "$$EXISTING_PG_PWD" ]; then \
			POSTGRES_PASSWORD="$$EXISTING_PG_PWD"; \
			echo "  $(GREEN)✓ Using existing PostgreSQL password from helm secret$(NC)"; \
		else \
			EXISTING_PG_PWD=$$(kubectl get secret bifrost-postgresql-password -n $(K8S_NAMESPACE) -o jsonpath='{.data.password}' 2>/dev/null | base64 -d 2>/dev/null || true); \
			if [ -n "$$EXISTING_PG_PWD" ]; then \
				POSTGRES_PASSWORD="$$EXISTING_PG_PWD"; \
				echo "  $(GREEN)✓ Using existing PostgreSQL password$(NC)"; \
			else \
				POSTGRES_PASSWORD=$$(openssl rand -base64 24 | tr -d '/+=' | head -c 24); \
				kubectl create secret generic bifrost-postgresql-password \
					--from-literal=password="$$POSTGRES_PASSWORD" \
					-n $(K8S_NAMESPACE) --dry-run=client -o yaml | kubectl apply -f - >/dev/null 2>&1; \
				echo "  $(GREEN)✓ Created new PostgreSQL password secret$(NC)"; \
			fi; \
		fi; \
	fi; \
	HELM_EXTRA_ARGS=""; \
	if [ "$$CONFIG_STORE" = "no" ]; then \
		HELM_EXTRA_ARGS="$$HELM_EXTRA_ARGS --set storage.configStore.enabled=false"; \
	elif [ "$$CONFIG_STORE" = "sqlite" ]; then \
		HELM_EXTRA_ARGS="$$HELM_EXTRA_ARGS --set storage.configStore.enabled=true"; \
	elif [ "$$CONFIG_STORE" = "postgres" ]; then \
		HELM_EXTRA_ARGS="$$HELM_EXTRA_ARGS --set storage.configStore.enabled=true"; \
	fi; \
	if [ "$$LOG_STORE" = "no" ]; then \
		HELM_EXTRA_ARGS="$$HELM_EXTRA_ARGS --set storage.logsStore.enabled=false"; \
	elif [ "$$LOG_STORE" = "sqlite" ]; then \
		HELM_EXTRA_ARGS="$$HELM_EXTRA_ARGS --set storage.logsStore.enabled=true"; \
	elif [ "$$LOG_STORE" = "postgres" ]; then \
		HELM_EXTRA_ARGS="$$HELM_EXTRA_ARGS --set storage.logsStore.enabled=true"; \
	fi; \
	if [ "$$NEEDS_POSTGRES" = "true" ]; then \
		HELM_EXTRA_ARGS="$$HELM_EXTRA_ARGS --set storage.mode=postgres --set postgresql.enabled=true --set postgresql.auth.password=$$POSTGRES_PASSWORD"; \
	elif [ "$$CONFIG_STORE" = "no" ] && [ "$$LOG_STORE" = "no" ]; then \
		HELM_EXTRA_ARGS="$$HELM_EXTRA_ARGS --set postgresql.enabled=false"; \
	else \
		HELM_EXTRA_ARGS="$$HELM_EXTRA_ARGS --set storage.mode=sqlite --set postgresql.enabled=false"; \
	fi; \
	CHART_REF="./helm-charts/bifrost"; \
	if [ -n "$$HELM_VERSION" ]; then \
		echo "  $(CYAN)Setting up Helm repository...$(NC)"; \
		helm repo add bifrost https://maximhq.github.io/bifrost/helm-charts >/dev/null 2>&1 || true; \
		helm repo update >/dev/null 2>&1; \
		CHART_REF="bifrost/bifrost --version $$HELM_VERSION"; \
		echo "  $(GREEN)✓ Using remote chart v$$HELM_VERSION$(NC)"; \
	fi; \
	( \
		helm upgrade --install $(K8S_RELEASE_NAME) $$CHART_REF \
			-n $(K8S_NAMESPACE) \
			--create-namespace \
			-f ./recipes/values-local-k8s.yaml \
			--set image.tag=$$IMAGE_TAG \
			--set 'ingress.hosts[0].host=$(K8S_HOST)' \
			--set 'ingress.tls[0].hosts[0]=$(K8S_HOST)' \
			$$HELM_EXTRA_ARGS \
			--wait --timeout 300s > /tmp/bifrost-helm-output.log 2>&1 & \
		CMD_PID=$$!; \
		ELAPSED=0; \
		while kill -0 $$CMD_PID 2>/dev/null; do \
			BIFROST_STATUS=$$(kubectl get pods -n $(K8S_NAMESPACE) -l app.kubernetes.io/name=bifrost -o jsonpath='{.items[0].status.phase}' 2>/dev/null || echo "Pending"); \
			if [ "$$NEEDS_POSTGRES" = "true" ]; then \
				PG_STATUS=$$(kubectl get pods -n $(K8S_NAMESPACE) -l app.kubernetes.io/component=database -o jsonpath='{.items[0].status.phase}' 2>/dev/null || echo "Pending"); \
				STATUS_MSG="postgres: $$PG_STATUS | bifrost: $$BIFROST_STATUS"; \
			else \
				STATUS_MSG="bifrost: $$BIFROST_STATUS"; \
			fi; \
			SPINNER_CHARS="⠋ ⠙ ⠹ ⠸ ⠼ ⠴ ⠦ ⠧ ⠇ ⠏"; \
			CHAR=$$(echo $$SPINNER_CHARS | cut -d' ' -f$$((ELAPSED % 10 + 1))); \
			printf "\r  $(CYAN)$$CHAR Deploying... [%ds] %s$(NC)    " "$$ELAPSED" "$$STATUS_MSG"; \
			ELAPSED=$$((ELAPSED + 1)); \
			sleep 1; \
		done; \
		wait $$CMD_PID; \
		EXIT_CODE=$$?; \
		printf "\r%-80s\r" " "; \
		if [ $$EXIT_CODE -eq 0 ]; then \
			echo "  $(GREEN)✓ Bifrost deployed [$$ELAPSED seconds]$(NC)"; \
		else \
			echo "  $(RED)✗ Deployment failed$(NC)"; \
			echo ""; \
			echo "  $(YELLOW)Helm output:$(NC)"; \
			cat /tmp/bifrost-helm-output.log | sed 's/^/    /'; \
			echo ""; \
			echo "  $(YELLOW)Debug commands:$(NC)"; \
			echo "    kubectl get pods -n $(K8S_NAMESPACE)"; \
			echo "    kubectl logs -l app.kubernetes.io/name=bifrost -n $(K8S_NAMESPACE)"; \
			rm -f /tmp/bifrost-helm-output.log; \
			exit 1; \
		fi; \
		rm -f /tmp/bifrost-helm-output.log \
	); \
	rm -f /tmp/bifrost-config-store.tmp /tmp/bifrost-log-store.tmp /tmp/bifrost-image-tag.tmp /tmp/bifrost-helm-version.tmp

local-k8s-update-key: ## Update OpenAI API key (Usage: make local-k8s-update-key OPENAI_API_KEY='sk-...')
	@if [ -z "$(OPENAI_API_KEY)" ]; then \
		echo "$(RED)Error: OPENAI_API_KEY is required$(NC)"; \
		echo "$(YELLOW)Usage: make local-k8s-update-key OPENAI_API_KEY='sk-your-key'$(NC)"; \
		exit 1; \
	fi
	@kubectl create secret generic bifrost-provider-keys \
		--from-literal=openai-api-key='$(OPENAI_API_KEY)' \
		-n $(K8S_NAMESPACE) --dry-run=client -o yaml | kubectl apply -f - >/dev/null 2>&1
	@echo "$(GREEN)✓ Updated bifrost-provider-keys$(NC)"
	@echo "$(YELLOW)Restarting Bifrost to pick up new key...$(NC)"
	@kubectl rollout restart deployment/$(K8S_RELEASE_NAME) -n $(K8S_NAMESPACE) >/dev/null 2>&1
	@echo "$(GREEN)✓ Bifrost restarted$(NC)"

local-k8s-status: ## Check local K8s deployment status
	@echo "$(BLUE)Bifrost Local K8s Status$(NC)"
	@echo ""
	@echo "$(CYAN)Pods:$(NC)"
	@kubectl get pods -n $(K8S_NAMESPACE) -o wide
	@echo ""
	@echo "$(CYAN)Services:$(NC)"
	@kubectl get svc -n $(K8S_NAMESPACE)
	@echo ""
	@echo "$(CYAN)Ingress:$(NC)"
	@kubectl get ingress -n $(K8S_NAMESPACE)
	@echo ""

local-k8s-logs: ## View Bifrost logs
	@kubectl logs -l app.kubernetes.io/name=bifrost -n $(K8S_NAMESPACE) -f

local-k8s-port-forward: ## Port-forward Bifrost service to localhost:8080
	@echo "$(CYAN)Port-forwarding to http://localhost:8080$(NC)"
	@echo "$(YELLOW)Press Ctrl+C to stop$(NC)"
	@kubectl port-forward svc/$(K8S_RELEASE_NAME) 8080:8080 -n $(K8S_NAMESPACE)

cleanup-local-k8s: ## Remove Bifrost from local K8s
	@echo "$(YELLOW)Cleaning up Bifrost deployment...$(NC)"
	@helm uninstall $(K8S_RELEASE_NAME) -n $(K8S_NAMESPACE) 2>/dev/null || true
	@kubectl delete namespace $(K8S_NAMESPACE) 2>/dev/null || true
	@echo "$(GREEN)✓ Cleanup complete$(NC)"
