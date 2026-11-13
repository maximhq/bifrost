# revops-copilot: every LLM, MCP and A2A hop goes through Bifrost.
UV ?= uvx uv@0.11.0
RUN = $(UV) run
AGENTS = revops-copilot support-analyst report-writer publisher

.PHONY: help install test chat agents agents-ngrok agents-cloudflared agent preflight ask demo demo-all trace

help: ## Show targets
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-12s %s\n", $$1, $$2}'

install: ## Create the venv from uv.lock
	$(UV) sync --frozen

test: ## Unit tests (no Bifrost needed)
	$(RUN) pytest -q

chat: ## Interactive prompt: type a request, watch every step live
	$(RUN) revops chat

agents: ## Run all four agents in the foreground (Ctrl-C stops them; one exiting stops all)
	@pids=""; trap 'kill $$pids 2>/dev/null; exit 130' INT TERM; \
	for a in $(AGENTS); do $(RUN) revops serve $$a & pids="$$pids $$!"; done; \
	while :; do \
		for p in $$pids; do \
			if ! kill -0 $$p 2>/dev/null; then \
				wait $$p; code=$$?; [ $$code -eq 0 ] && code=1; \
				echo "an agent exited (status $$code); stopping the others" >&2; \
				kill $$pids 2>/dev/null; wait; exit $$code; \
			fi; \
		done; \
		sleep 1; \
	done

agents-ngrok: ## Run all four agents behind ngrok (4 endpoints: paid plan)
	$(RUN) revops ngrok

agents-cloudflared: ## Run all four agents behind free Cloudflare quick tunnels
	$(RUN) revops cloudflared

agent: ## Run one agent: make agent A=publisher
	$(RUN) revops serve $(A)

preflight: ## Check the Bifrost setup (read-only)
	$(RUN) revops preflight

ask: ## Send a job: make ask Q="Prepare the Q3 report ..."
	$(RUN) revops ask "$(Q)"

demo: ## Run one scenario: make demo S=prompt-injection (no S lists them)
	$(RUN) revops demo $(S)

demo-all: ## Run every scenario; fails if any scenario fails
	@failed=""; for s in scenarios/*.yaml; do \
		name=$$(basename $$s .yaml); $(RUN) revops demo $$name || failed="$$failed $$name"; \
	done; \
	if [ -n "$$failed" ]; then echo "failed scenarios:$$failed" >&2; exit 1; fi

trace: ## Per-surface timeline: make trace ID=<x-bf-session-id>
	$(RUN) revops trace $(ID)
