# Cascade-tag Recipe
# Creates git tags for a chosen tier and every tier below it in the
# hierarchy: core -> framework -> plugins/* -> transports, then pushes
# them to the remote. Include via: include recipes/tag.mk

.PHONY: cascade-tag cascade-untag

# Plugin names, discovered at parse time so new plugins are picked up automatically.
PLUGIN_NAMES := $(patsubst plugins/%/,%,$(wildcard plugins/*/))

# Ordered tiers to tag, based on FROM.
ifeq ($(FROM),core)
  CASCADE_TIERS := core framework plugins transports
else ifeq ($(FROM),framework)
  CASCADE_TIERS := framework plugins transports
else ifeq ($(FROM),plugins)
  CASCADE_TIERS := plugins transports
else ifeq ($(FROM),transports)
  CASCADE_TIERS := transports
else
  CASCADE_TIERS :=
endif

# Expand tiers to concrete tag names of the form <module>/v<version>.
cascade-tag-names = $(strip \
  $(foreach t,$(CASCADE_TIERS),\
    $(if $(filter core,$(t)),core/$(TAG),)\
    $(if $(filter framework,$(t)),framework/$(TAG),)\
    $(if $(filter plugins,$(t)),$(foreach p,$(PLUGIN_NAMES),plugins/$(p)/$(TAG)),)\
    $(if $(filter transports,$(t)),transports/$(TAG),)))

REMOTE ?= origin
MESSAGE ?= Cascade release $(TAG) from $(FROM)

cascade-tag: ## Cascade-create git tags from FROM down and push (Usage: make cascade-tag FROM=<core|framework|plugins|transports> TAG=[prefix-]vX.Y.Z[-suffix] [REMOTE=origin] [MESSAGE="..."])
	@echo "$(BLUE)Cascade-tag starting...$(NC)"
	@if [ -z "$(FROM)" ] || [ -z "$(TAG)" ]; then \
		echo "$(RED)Error: FROM and TAG are required$(NC)"; \
		echo "$(YELLOW)Usage: make cascade-tag FROM=<core|framework|plugins|transports> TAG=vX.Y.Z$(NC)"; \
		exit 1; \
	fi
	@case "$(FROM)" in \
		core|framework|plugins|transports) ;; \
		*) echo "$(RED)Error: FROM must be one of core, framework, plugins, transports (got '$(FROM)')$(NC)"; exit 1 ;; \
	esac
	@echo "$(TAG)" | grep -Eq '^([A-Za-z0-9][A-Za-z0-9.]*-)?v[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.+-]+)?$$' || { \
		echo "$(RED)Error: TAG must look like [prefix-]vX.Y.Z[-suffix] (got '$(TAG)')$(NC)"; exit 1; \
	}
	@git diff-index --quiet HEAD -- || { \
		echo "$(RED)Error: working tree is not clean. Commit or stash changes first.$(NC)"; exit 1; \
	}
	@echo "$(YELLOW)Fetching tags from $(REMOTE)...$(NC)"
	@git fetch --tags $(REMOTE) > /dev/null 2>&1 || true
	@conflicts=""; \
	for t in $(cascade-tag-names); do \
		if git rev-parse -q --verify "refs/tags/$$t" > /dev/null; then \
			conflicts="$$conflicts $$t(local)"; \
		fi; \
		if git ls-remote --tags --exit-code $(REMOTE) "$$t" > /dev/null 2>&1; then \
			conflicts="$$conflicts $$t(remote)"; \
		fi; \
	done; \
	if [ -n "$$conflicts" ]; then \
		echo "$(RED)Error: the following tags already exist:$(NC)"; \
		for c in $$conflicts; do echo "  - $$c"; done; \
		exit 1; \
	fi
	@echo ""
	@echo "$(CYAN)The following $(words $(cascade-tag-names)) tag(s) will be created at HEAD ($$(git rev-parse --short HEAD)) and pushed to $(REMOTE):$(NC)"
	@for t in $(cascade-tag-names); do echo "  $(GREEN)+$(NC) $$t"; done
	@echo ""
	@printf "Proceed? [y/N]: "; read response; \
	case "$$response" in \
		[yY]|[yY][eE][sS]) ;; \
		*) echo "$(YELLOW)Aborted.$(NC)"; exit 1 ;; \
	esac
	@created=""; \
	for t in $(cascade-tag-names); do \
		if git tag -a "$$t" -m "$(MESSAGE)"; then \
			echo "$(GREEN)✓ created $$t$(NC)"; \
			created="$$created $$t"; \
		else \
			echo "$(RED)✗ failed to create $$t$(NC)"; \
			if [ -n "$$created" ]; then \
				echo "$(YELLOW)Locally created tags so far:$$created$(NC)"; \
				echo "$(YELLOW)Delete them with: git tag -d$$created$(NC)"; \
			fi; \
			exit 1; \
		fi; \
	done
	@echo ""
	@echo "$(YELLOW)Pushing tags to $(REMOTE)...$(NC)"
	@if git push $(REMOTE) $(cascade-tag-names); then \
		echo "$(GREEN)✓ All tags pushed to $(REMOTE)$(NC)"; \
	else \
		echo "$(RED)✗ Push failed. Local tags remain — retry with: git push $(REMOTE) $(cascade-tag-names)$(NC)"; \
		exit 1; \
	fi
	@echo ""
	@echo "$(GREEN)✓ Cascade-tag complete: $(words $(cascade-tag-names)) tag(s) created and pushed.$(NC)"

cascade-untag: ## Cascade-delete git tags from FROM down, locally and on the remote (Usage: make cascade-untag FROM=<core|framework|plugins|transports> TAG=[prefix-]vX.Y.Z[-suffix] [REMOTE=origin])
	@echo "$(BLUE)Cascade-untag starting...$(NC)"
	@if [ -z "$(FROM)" ] || [ -z "$(TAG)" ]; then \
		echo "$(RED)Error: FROM and TAG are required$(NC)"; \
		echo "$(YELLOW)Usage: make cascade-untag FROM=<core|framework|plugins|transports> TAG=vX.Y.Z$(NC)"; \
		exit 1; \
	fi
	@case "$(FROM)" in \
		core|framework|plugins|transports) ;; \
		*) echo "$(RED)Error: FROM must be one of core, framework, plugins, transports (got '$(FROM)')$(NC)"; exit 1 ;; \
	esac
	@echo "$(TAG)" | grep -Eq '^([A-Za-z0-9][A-Za-z0-9.]*-)?v[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.+-]+)?$$' || { \
		echo "$(RED)Error: TAG must look like [prefix-]vX.Y.Z[-suffix] (got '$(TAG)')$(NC)"; exit 1; \
	}
	@echo "$(YELLOW)Fetching tags from $(REMOTE)...$(NC)"
	@git fetch --tags $(REMOTE) > /dev/null 2>&1 || true
	@local_tags=""; remote_tags=""; missing=""; \
	for t in $(cascade-tag-names); do \
		has_local=0; has_remote=0; \
		if git rev-parse -q --verify "refs/tags/$$t" > /dev/null; then has_local=1; fi; \
		if git ls-remote --tags --exit-code $(REMOTE) "$$t" > /dev/null 2>&1; then has_remote=1; fi; \
		if [ $$has_local -eq 1 ]; then local_tags="$$local_tags $$t"; fi; \
		if [ $$has_remote -eq 1 ]; then remote_tags="$$remote_tags $$t"; fi; \
		if [ $$has_local -eq 0 ] && [ $$has_remote -eq 0 ]; then missing="$$missing $$t"; fi; \
	done; \
	echo ""; \
	if [ -z "$$local_tags" ] && [ -z "$$remote_tags" ]; then \
		echo "$(YELLOW)No matching tags found locally or on $(REMOTE). Nothing to delete.$(NC)"; \
		exit 0; \
	fi; \
	echo "$(CYAN)The following tag(s) will be deleted:$(NC)"; \
	for t in $(cascade-tag-names); do \
		loc=""; rem=""; \
		case " $$local_tags " in *" $$t "*) loc="local";; esac; \
		case " $$remote_tags " in *" $$t "*) rem="remote";; esac; \
		if [ -n "$$loc" ] || [ -n "$$rem" ]; then \
			where="$$loc"; [ -n "$$loc" ] && [ -n "$$rem" ] && where="local+remote"; [ -z "$$loc" ] && where="remote"; \
			echo "  $(RED)-$(NC) $$t  $(YELLOW)[$$where]$(NC)"; \
		fi; \
	done; \
	if [ -n "$$missing" ]; then \
		echo ""; \
		echo "$(YELLOW)Not found (will be skipped):$(NC)"; \
		for t in $$missing; do echo "  · $$t"; done; \
	fi; \
	echo ""; \
	printf "$(RED)This will permanently delete the tags above on $(REMOTE). Proceed? [y/N]: $(NC)"; read response; \
	case "$$response" in \
		[yY]|[yY][eE][sS]) ;; \
		*) echo "$(YELLOW)Aborted.$(NC)"; exit 1 ;; \
	esac; \
	if [ -n "$$local_tags" ]; then \
		echo ""; \
		echo "$(YELLOW)Deleting local tags...$(NC)"; \
		for t in $$local_tags; do \
			if git tag -d "$$t" > /dev/null; then \
				echo "$(GREEN)✓ local  $$t$(NC)"; \
			else \
				echo "$(RED)✗ local  $$t$(NC)"; \
			fi; \
		done; \
	fi; \
	if [ -n "$$remote_tags" ]; then \
		echo ""; \
		echo "$(YELLOW)Deleting remote tags on $(REMOTE)...$(NC)"; \
		if git push $(REMOTE) --delete $$remote_tags; then \
			echo "$(GREEN)✓ Remote tags deleted$(NC)"; \
		else \
			echo "$(RED)✗ Remote delete failed — retry with: git push $(REMOTE) --delete$$remote_tags$(NC)"; \
			exit 1; \
		fi; \
	fi; \
	echo ""; \
	echo "$(GREEN)✓ Cascade-untag complete.$(NC)"
