fix: properly set bifrost version in metrics
feat: added team_id, team_name, customer_id and customer_name labels to otel metrics
fix: skip adding google/ prefix for custom fine-tuned models in vertex provider (for genai integration)
fix: deep copy inputs in semantic cache plugin to not mutate the original request