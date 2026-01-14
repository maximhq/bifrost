# AWS ECS Deployment Recipe
# Include this in your main Makefile with: include recipes/ecs.mk

# Configuration variables
ECS_CLUSTER_NAME ?= bifrost-cluster
ECS_SERVICE_NAME ?= bifrost-service
ECS_TASK_FAMILY ?= bifrost-task
IMAGE_TAG ?= latest
LAUNCH_TYPE ?= FARGATE
SECRET_BACKEND ?= secretsmanager
AWS_REGION ?= us-east-1
VPC_ID ?=
SUBNET_IDS ?=
SECURITY_GROUP_IDS ?=
TARGET_GROUP_ARN ?=
CONTAINER_PORT ?= 8080
SECRET_NAME ?= bifrost/config
SECRET_ARN ?=
EXECUTION_ROLE_ARN ?=
TASK_ROLE_ARN ?=

# Configuration JSON file path (optional - provide path to your config.json file)
# Example: CONFIG_JSON_FILE=/path/to/config.json
CONFIG_JSON_FILE ?=

.PHONY: deploy-ecs list-ecs-network-resources check-ecs-prerequisites create-ecs-secret register-ecs-task-definition create-ecs-service update-ecs-service tail-ecs-logs ecs-status get-ecs-url cleanup-ecs

deploy-ecs: ## Deploy Bifrost to ECS (Usage: make deploy-ecs SUBNET_IDS='...' SECURITY_GROUP_IDS='...' [CONFIG_JSON_FILE='...'])
	@echo "$(BLUE)Starting ECS deployment...$(NC)"
	@echo ""
	@$(MAKE) check-ecs-prerequisites
	@if [ -n "$(CONFIG_JSON_FILE)" ]; then \
		$(MAKE) create-ecs-secret; \
	else \
		echo "$(YELLOW)No CONFIG_JSON_FILE provided, skipping secret creation$(NC)"; \
	fi
	@$(MAKE) register-ecs-task-definition
	@$(MAKE) create-ecs-service
	@echo ""
	@echo "$(GREEN)✓ ECS deployment complete!$(NC)"

list-ecs-network-resources: ## List available VPCs, subnets and security groups for ECS deployment
	@echo "$(BLUE)Listing available network resources in region $(AWS_REGION)...$(NC)"
	@echo ""
	@# Check if AWS CLI is configured
	@aws sts get-caller-identity > /dev/null 2>&1 || \
		(echo "$(RED)Error: AWS CLI is not configured.$(NC)" && \
		echo "$(YELLOW)Run: aws configure$(NC)" && exit 1)
	@echo "$(CYAN)Available VPCs:$(NC)"
	@aws ec2 describe-vpcs \
		--region $(AWS_REGION) \
		--query 'Vpcs[*].[VpcId,CidrBlock,IsDefault,Tags[?Key==`Name`].Value|[0]]' \
		--output table
	@echo ""
	@echo "$(CYAN)Available Subnets:$(NC)"
	@aws ec2 describe-subnets \
		--region $(AWS_REGION) \
		--query 'Subnets[*].[SubnetId,AvailabilityZone,VpcId,CidrBlock,Tags[?Key==`Name`].Value|[0]]' \
		--output table
	@echo ""
	@echo "$(CYAN)Available Security Groups:$(NC)"
	@aws ec2 describe-security-groups \
		--region $(AWS_REGION) \
		--query 'SecurityGroups[*].[GroupId,GroupName,VpcId,Description]' \
		--output table
	@echo ""
	@echo "$(YELLOW)Usage (Option 1 - Recommended):$(NC)"
	@echo "  Use VPC ID to auto-fetch all subnets:"
	@echo "  $(GREEN)make deploy-ecs VPC_ID='vpc-xxx' SECURITY_GROUP_IDS='sg-xxx'$(NC)"
	@echo ""
	@echo "$(YELLOW)Usage (Option 2):$(NC)"
	@echo "  Specify subnet IDs manually:"
	@echo "  $(GREEN)make deploy-ecs SUBNET_IDS='subnet-xxx,subnet-yyy' SECURITY_GROUP_IDS='sg-xxx'$(NC)"

check-ecs-prerequisites: ## Check ECS deployment prerequisites
	@echo "$(YELLOW)Checking prerequisites...$(NC)"
	@# Check if AWS CLI is installed
	@which aws > /dev/null || (echo "$(RED)Error: AWS CLI is not installed.$(NC)" && \
		echo "$(YELLOW)Please install AWS CLI first.$(NC)" && \
		echo "$(CYAN)Documentation: https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html$(NC)" && \
		exit 1)
	@echo "$(GREEN)✓ AWS CLI is installed$(NC)"
	@# Check if AWS CLI is configured
	@aws sts get-caller-identity > /dev/null 2>&1 || \
		(echo "$(RED)Error: AWS CLI is not configured.$(NC)" && \
		echo "$(YELLOW)Please configure AWS CLI with your credentials.$(NC)" && \
		echo "$(CYAN)Run: aws configure$(NC)" && \
		echo "$(CYAN)Documentation: https://docs.aws.amazon.com/cli/latest/userguide/cli-chap-configure.html$(NC)" && \
		exit 1)
	@echo "$(GREEN)✓ AWS CLI is configured$(NC)"
	@# Check if cluster exists
	@aws ecs describe-clusters --clusters $(ECS_CLUSTER_NAME) --region $(AWS_REGION) > /dev/null 2>&1 || \
		(echo "$(RED)Error: ECS cluster '$(ECS_CLUSTER_NAME)' not found$(NC)" && exit 1)
	@echo "$(GREEN)✓ ECS cluster '$(ECS_CLUSTER_NAME)' exists$(NC)"
	@# Check/fetch execution role ARN if not provided and using Fargate
	@if [ -z "$(EXECUTION_ROLE_ARN)" ]; then \
		echo "$(CYAN)No EXECUTION_ROLE_ARN provided, checking for default role...$(NC)"; \
		ACCOUNT_ID=$$(aws sts get-caller-identity --query Account --output text 2>/dev/null); \
		DEFAULT_ROLE_ARN="arn:aws:iam::$$ACCOUNT_ID:role/ecsTaskExecutionRole"; \
		if aws iam get-role --role-name ecsTaskExecutionRole --region $(AWS_REGION) > /dev/null 2>&1; then \
			echo "$$DEFAULT_ROLE_ARN" > /tmp/ecs-execution-role.tmp; \
			echo "$(GREEN)✓ Found default execution role: ecsTaskExecutionRole$(NC)"; \
		else \
			echo ""; \
			echo "$(RED)Error: No execution role found$(NC)"; \
			echo ""; \
			echo "$(YELLOW)ECS tasks require an execution role for CloudWatch logs and pulling images.$(NC)"; \
			echo ""; \
			echo "$(CYAN)Option 1 - Create the default role:$(NC)"; \
			echo "  $(GREEN)aws iam create-role --role-name ecsTaskExecutionRole \\"; \
			echo "    --assume-role-policy-document '{"; \
			echo "      \"Version\": \"2012-10-17\","; \
			echo "      \"Statement\": [{"; \
			echo "        \"Effect\": \"Allow\","; \
			echo "        \"Principal\": {\"Service\": \"ecs-tasks.amazonaws.com\"},"; \
			echo "        \"Action\": \"sts:AssumeRole\""; \
			echo "      }]"; \
			echo "    }'$(NC)"; \
			echo ""; \
			echo "  $(GREEN)aws iam attach-role-policy --role-name ecsTaskExecutionRole \\"; \
			echo "    --policy-arn arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy$(NC)"; \
			echo ""; \
			echo "$(CYAN)Option 2 - Specify an existing role:$(NC)"; \
			echo "  $(GREEN)make deploy-ecs EXECUTION_ROLE_ARN='arn:aws:iam::ACCOUNT:role/YOUR_ROLE' ...$(NC)"; \
			echo ""; \
			exit 1; \
		fi; \
	else \
		echo "$(GREEN)✓ Using provided execution role$(NC)"; \
	fi
	@# Fetch subnets from VPC if VPC_ID is provided but SUBNET_IDS is not
	@if [ -n "$(VPC_ID)" ] && [ -z "$(SUBNET_IDS)" ]; then \
		echo "$(CYAN)Fetching subnets from VPC $(VPC_ID)...$(NC)"; \
		FETCHED_SUBNETS=$$(aws ec2 describe-subnets \
			--region $(AWS_REGION) \
			--filters "Name=vpc-id,Values=$(VPC_ID)" \
			--query 'Subnets[*].SubnetId' \
			--output text 2>/dev/null | tr '\t' ','); \
		if [ -z "$$FETCHED_SUBNETS" ]; then \
			echo "$(RED)Error: No subnets found in VPC $(VPC_ID)$(NC)"; \
			exit 1; \
		fi; \
		echo "$$FETCHED_SUBNETS" > /tmp/ecs-subnets.tmp; \
		echo "$(GREEN)✓ Found subnets: $$FETCHED_SUBNETS$(NC)"; \
	fi
	@# Check if required network parameters are provided
	@if [ -z "$(SUBNET_IDS)" ] && [ ! -f /tmp/ecs-subnets.tmp ] && [ -z "$(VPC_ID)" ]; then \
		echo ""; \
		echo "$(RED)Error: Network configuration is required$(NC)"; \
		echo ""; \
		echo "$(YELLOW)You must provide either:$(NC)"; \
		echo "  - VPC_ID (will auto-fetch all subnets in VPC)"; \
		echo "  - SUBNET_IDS (specific subnet IDs)"; \
		echo ""; \
		echo "$(CYAN)To list available VPCs and network resources, run:$(NC)"; \
		echo "  $(GREEN)make list-ecs-network-resources$(NC)"; \
		echo ""; \
		echo "$(CYAN)Then deploy with VPC ID (recommended):$(NC)"; \
		echo "  $(GREEN)make deploy-ecs VPC_ID='vpc-xxx' SECURITY_GROUP_IDS='sg-xxx'$(NC)"; \
		echo ""; \
		echo "$(CYAN)Or deploy with specific subnet IDs:$(NC)"; \
		echo "  $(GREEN)make deploy-ecs SUBNET_IDS='subnet-xxx,subnet-yyy' SECURITY_GROUP_IDS='sg-xxx'$(NC)"; \
		echo ""; \
		exit 1; \
	fi
	@if [ -z "$(SECURITY_GROUP_IDS)" ]; then \
		echo ""; \
		echo "$(RED)Error: SECURITY_GROUP_IDS is required$(NC)"; \
		echo ""; \
		echo "$(CYAN)To list available security groups, run:$(NC)"; \
		echo "  $(GREEN)make list-ecs-network-resources$(NC)"; \
		echo ""; \
		exit 1; \
	fi
	@echo "$(GREEN)✓ Network configuration ready$(NC)"

create-ecs-secret: ## Create configuration secret in AWS (Secrets Manager or SSM)
	@if [ -z "$(CONFIG_JSON_FILE)" ]; then \
		echo "$(RED)Error: CONFIG_JSON_FILE is required for secret creation$(NC)"; \
		echo "$(YELLOW)Provide CONFIG_JSON_FILE as path to your Bifrost config.json file$(NC)"; \
		exit 1; \
	fi
	@if [ ! -f "$(CONFIG_JSON_FILE)" ]; then \
		echo "$(RED)Error: Config file not found: $(CONFIG_JSON_FILE)$(NC)"; \
		exit 1; \
	fi
	@echo "$(YELLOW)Creating configuration secret...$(NC)"
	@echo "$(CYAN)Reading config from: $(CONFIG_JSON_FILE)$(NC)"
	@if [ "$(SECRET_BACKEND)" = "secretsmanager" ]; then \
		echo "$(CYAN)Using AWS Secrets Manager...$(NC)"; \
		aws secretsmanager describe-secret --secret-id $(SECRET_NAME) --region $(AWS_REGION) > /dev/null 2>&1 && \
			(echo "$(YELLOW)Secret already exists, updating...$(NC)" && \
			aws secretsmanager update-secret \
				--secret-id $(SECRET_NAME) \
				--secret-string file://$(CONFIG_JSON_FILE) \
				--region $(AWS_REGION) > /dev/null) || \
			(echo "$(YELLOW)Creating new secret...$(NC)" && \
			aws secretsmanager create-secret \
				--name $(SECRET_NAME) \
				--secret-string file://$(CONFIG_JSON_FILE) \
				--region $(AWS_REGION) > /dev/null); \
		echo "$(GREEN)✓ Secret created/updated in Secrets Manager: $(SECRET_NAME)$(NC)"; \
	elif [ "$(SECRET_BACKEND)" = "ssm" ]; then \
		echo "$(CYAN)Using AWS Systems Manager Parameter Store...$(NC)"; \
		aws ssm put-parameter \
			--name $(SECRET_NAME) \
			--value file://$(CONFIG_JSON_FILE) \
			--type SecureString \
			--overwrite \
			--region $(AWS_REGION) > /dev/null; \
		echo "$(GREEN)✓ Parameter created/updated in SSM: $(SECRET_NAME)$(NC)"; \
	else \
		echo "$(RED)Error: SECRET_BACKEND must be 'secretsmanager' or 'ssm'$(NC)"; \
		exit 1; \
	fi

register-ecs-task-definition: ## Register ECS task definition
	@echo "$(YELLOW)Registering ECS task definition...$(NC)"
	@echo "$(CYAN)Launch type: $(LAUNCH_TYPE)$(NC)"
	@# Create CloudWatch log group if it doesn't exist
	@echo "$(CYAN)Ensuring CloudWatch log group exists...$(NC)"
	@aws logs create-log-group \
		--log-group-name /ecs/$(ECS_TASK_FAMILY) \
		--region $(AWS_REGION) 2>/dev/null || true
	@echo "$(GREEN)✓ CloudWatch log group ready: /ecs/$(ECS_TASK_FAMILY)$(NC)"
	@# Get secret ARN if CONFIG_JSON_FILE was provided
	@if [ -n "$(CONFIG_JSON_FILE)" ]; then \
		echo "$(CYAN)Secret backend: $(SECRET_BACKEND)$(NC)"; \
	else \
		echo "$(YELLOW)No CONFIG_JSON_FILE provided, deploying without secret$(NC)"; \
	fi
	$(eval SECRET_VALUE_ARN := $(shell \
		if [ -n "$(CONFIG_JSON_FILE)" ]; then \
			if [ "$(SECRET_BACKEND)" = "secretsmanager" ]; then \
				if [ -z "$(SECRET_ARN)" ]; then \
					aws secretsmanager describe-secret --secret-id $(SECRET_NAME) --region $(AWS_REGION) --query 'ARN' --output text 2>/dev/null; \
				else \
					echo "$(SECRET_ARN)"; \
				fi; \
			elif [ "$(SECRET_BACKEND)" = "ssm" ]; then \
				if [ -z "$(SECRET_ARN)" ]; then \
					aws ssm get-parameter --name $(SECRET_NAME) --region $(AWS_REGION) --query 'Parameter.ARN' --output text 2>/dev/null; \
				else \
					echo "$(SECRET_ARN)"; \
				fi; \
			fi; \
		fi))
	@if [ -n "$(CONFIG_JSON_FILE)" ] && [ -z "$(SECRET_VALUE_ARN)" ]; then \
		echo "$(RED)Error: Could not retrieve secret ARN$(NC)"; \
		exit 1; \
	fi
	@if [ -n "$(SECRET_VALUE_ARN)" ]; then \
		echo "$(GREEN)✓ Secret ARN: $(SECRET_VALUE_ARN)$(NC)"; \
	fi
	@# Create task definition JSON using shell script for proper JSON formatting
	@TASK_CPU="256"; \
	TASK_MEMORY="512"; \
	if [ "$(LAUNCH_TYPE)" = "FARGATE" ]; then \
		TASK_CPU="512"; \
		TASK_MEMORY="1024"; \
	fi; \
	EXEC_ROLE="$(EXECUTION_ROLE_ARN)"; \
	if [ -z "$$EXEC_ROLE" ] && [ -f /tmp/ecs-execution-role.tmp ]; then \
		EXEC_ROLE=$$(cat /tmp/ecs-execution-role.tmp); \
	fi; \
	{ \
		echo '{'; \
		echo '  "family": "$(ECS_TASK_FAMILY)",'; \
		echo '  "networkMode": "awsvpc",'; \
		echo '  "requiresCompatibilities": ["$(LAUNCH_TYPE)"],'; \
		if [ "$(LAUNCH_TYPE)" = "FARGATE" ]; then \
			echo '  "cpu": "'$$TASK_CPU'",'; \
			echo '  "memory": "'$$TASK_MEMORY'",'; \
		fi; \
		if [ -n "$$EXEC_ROLE" ]; then \
			echo '  "executionRoleArn": "'$$EXEC_ROLE'",'; \
		fi; \
		if [ -n "$(TASK_ROLE_ARN)" ]; then \
			echo '  "taskRoleArn": "$(TASK_ROLE_ARN)",'; \
		fi; \
		echo '  "containerDefinitions": [{'; \
		echo '    "name": "bifrost",'; \
		echo '    "image": "maximhq/bifrost:$(IMAGE_TAG)",'; \
		echo '    "essential": true,'; \
		if [ -n "$(SECRET_VALUE_ARN)" ]; then \
			echo '    "entryPoint": ["/bin/sh", "-c"],'; \
			echo '    "command": ["if [ -n \"$$BIFROST_CONFIG\" ]; then echo \"$$BIFROST_CONFIG\" > /app/data/config.json; else echo \"ERROR: BIFROST_CONFIG not set\" >&2 && exit 1; fi && exec /app/docker-entrypoint.sh /app/main"],'; \
		fi; \
		echo '    "portMappings": [{'; \
		echo '      "containerPort": $(CONTAINER_PORT),'; \
		echo '      "protocol": "tcp"'; \
		echo '    }],'; \
		echo '    "environment": [],'; \
		if [ -n "$(SECRET_VALUE_ARN)" ]; then \
			echo '    "secrets": [{'; \
			echo '      "name": "BIFROST_CONFIG",'; \
			echo '      "valueFrom": "$(SECRET_VALUE_ARN)"'; \
			echo '    }],'; \
		fi; \
		echo '    "healthCheck": {'; \
		echo '      "command": ["CMD-SHELL", "wget --no-verbose --tries=1 --spider http://localhost:$(CONTAINER_PORT)/health || exit 1"],'; \
		echo '      "interval": 30,'; \
		echo '      "timeout": 5,'; \
		echo '      "retries": 3,'; \
		echo '      "startPeriod": 60'; \
		echo '    },'; \
		echo '    "logConfiguration": {'; \
		echo '      "logDriver": "awslogs",'; \
		echo '      "options": {'; \
		echo '        "awslogs-group": "/ecs/$(ECS_TASK_FAMILY)",'; \
		echo '        "awslogs-region": "$(AWS_REGION)",'; \
		echo '        "awslogs-stream-prefix": "bifrost"'; \
		echo '      }'; \
		echo '    }'; \
		echo '  }]'; \
		echo '}'; \
	} > /tmp/ecs-task-def.json
	@# Register task definition
	@aws ecs register-task-definition \
		--cli-input-json file:///tmp/ecs-task-def.json \
		--region $(AWS_REGION) > /dev/null
	@rm -f /tmp/ecs-task-def.json
	@echo "$(GREEN)✓ Task definition registered: $(ECS_TASK_FAMILY)$(NC)"

create-ecs-service: ## Create or update ECS service
	@echo "$(YELLOW)Creating/updating ECS service...$(NC)"
	@# Get subnet IDs from parameter or temp file
	@SUBNETS="$(SUBNET_IDS)"; \
	if [ -z "$$SUBNETS" ] && [ -f /tmp/ecs-subnets.tmp ]; then \
		SUBNETS=$$(cat /tmp/ecs-subnets.tmp); \
	fi; \
	if aws ecs describe-services --cluster $(ECS_CLUSTER_NAME) --services $(ECS_SERVICE_NAME) --region $(AWS_REGION) 2>/dev/null | grep -q "ACTIVE"; then \
		echo "$(YELLOW)Service exists, updating...$(NC)"; \
		$(MAKE) update-ecs-service; \
	else \
		echo "$(YELLOW)Creating new service...$(NC)"; \
		if [ -z "$(TARGET_GROUP_ARN)" ]; then \
			echo "$(CYAN)Creating service without load balancer...$(NC)"; \
			aws ecs create-service \
				--cluster $(ECS_CLUSTER_NAME) \
				--service-name $(ECS_SERVICE_NAME) \
				--task-definition $(ECS_TASK_FAMILY) \
				--desired-count 1 \
				--launch-type $(LAUNCH_TYPE) \
				--network-configuration "awsvpcConfiguration={subnets=[$$SUBNETS],securityGroups=[$(SECURITY_GROUP_IDS)],assignPublicIp=ENABLED}" \
				--region $(AWS_REGION) > /dev/null; \
		else \
			echo "$(CYAN)Creating service with load balancer...$(NC)"; \
			aws ecs create-service \
				--cluster $(ECS_CLUSTER_NAME) \
				--service-name $(ECS_SERVICE_NAME) \
				--task-definition $(ECS_TASK_FAMILY) \
				--desired-count 1 \
				--launch-type $(LAUNCH_TYPE) \
				--network-configuration "awsvpcConfiguration={subnets=[$$SUBNETS],securityGroups=[$(SECURITY_GROUP_IDS)],assignPublicIp=ENABLED}" \
				--load-balancers "targetGroupArn=$(TARGET_GROUP_ARN),containerName=bifrost,containerPort=$(CONTAINER_PORT)" \
				--health-check-grace-period-seconds 60 \
				--region $(AWS_REGION) > /dev/null; \
		fi; \
		echo "$(GREEN)✓ Service created: $(ECS_SERVICE_NAME)$(NC)"; \
		rm -f /tmp/ecs-subnets.tmp /tmp/ecs-execution-role.tmp; \
	fi
	@echo ""
	@echo "$(YELLOW)Waiting for service to stabilize...$(NC)"
	@echo "$(CYAN)This may take a few minutes...$(NC)"
	@aws ecs wait services-stable \
		--cluster $(ECS_CLUSTER_NAME) \
		--services $(ECS_SERVICE_NAME) \
		--region $(AWS_REGION) && \
		echo "$(GREEN)✓ Service is stable and running!$(NC)" || \
		(echo "$(RED)✗ Service failed to stabilize$(NC)" && exit 1)
	@echo ""
	@echo "$(CYAN)Deployment Status:$(NC)"
	@aws ecs describe-services \
		--cluster $(ECS_CLUSTER_NAME) \
		--services $(ECS_SERVICE_NAME) \
		--region $(AWS_REGION) \
		--query 'services[0].{Status:status,Running:runningCount,Desired:desiredCount,Pending:pendingCount}' \
		--output table
	@echo ""
	@echo "$(CYAN)Task Details:$(NC)"
	@TASK_ARN=$$(aws ecs list-tasks \
		--cluster $(ECS_CLUSTER_NAME) \
		--service-name $(ECS_SERVICE_NAME) \
		--region $(AWS_REGION) \
		--query 'taskArns[0]' \
		--output text 2>/dev/null); \
	if [ -n "$$TASK_ARN" ] && [ "$$TASK_ARN" != "None" ]; then \
		aws ecs describe-tasks \
			--cluster $(ECS_CLUSTER_NAME) \
			--tasks $$TASK_ARN \
			--region $(AWS_REGION) \
			--query 'tasks[0].{TaskARN:taskArn,LastStatus:lastStatus,HealthStatus:healthStatus,StartedAt:startedAt}' \
			--output table; \
		echo ""; \
		if [ -z "$(TARGET_GROUP_ARN)" ]; then \
			echo "$(CYAN)Public IP Address:$(NC)"; \
			ENI_ID=$$(aws ecs describe-tasks \
				--cluster $(ECS_CLUSTER_NAME) \
				--tasks $$TASK_ARN \
				--region $(AWS_REGION) \
				--query 'tasks[0].attachments[0].details[?name==`networkInterfaceId`].value' \
				--output text 2>/dev/null); \
			if [ -n "$$ENI_ID" ] && [ "$$ENI_ID" != "None" ]; then \
				PUBLIC_IP=$$(aws ec2 describe-network-interfaces \
					--network-interface-ids $$ENI_ID \
					--region $(AWS_REGION) \
					--query 'NetworkInterfaces[0].Association.PublicIp' \
					--output text 2>/dev/null); \
				if [ -n "$$PUBLIC_IP" ] && [ "$$PUBLIC_IP" != "None" ]; then \
					echo "  $$PUBLIC_IP"; \
					echo ""; \
					echo "$(GREEN)✓ Service is accessible at: http://$$PUBLIC_IP:$(CONTAINER_PORT)$(NC)"; \
					echo "$(CYAN)  Health check: http://$$PUBLIC_IP:$(CONTAINER_PORT)/health$(NC)"; \
				else \
					echo "  $(YELLOW)Public IP not yet assigned$(NC)"; \
				fi; \
			else \
				echo "  $(YELLOW)Network interface not found$(NC)"; \
			fi; \
			echo ""; \
		fi; \
		echo "$(CYAN)Recent logs (last 20 events):$(NC)"; \
		LOG_STREAM=$$(aws logs describe-log-streams \
			--log-group-name /ecs/$(ECS_TASK_FAMILY) \
			--order-by LastEventTime \
			--descending \
			--max-items 1 \
			--region $(AWS_REGION) \
			--query 'logStreams[0].logStreamName' \
			--output text 2>/dev/null); \
		if [ -n "$$LOG_STREAM" ] && [ "$$LOG_STREAM" != "None" ]; then \
			aws logs get-log-events \
				--log-group-name /ecs/$(ECS_TASK_FAMILY) \
				--log-stream-name $$LOG_STREAM \
				--limit 20 \
				--region $(AWS_REGION) \
				--query 'events[*].message' \
				--output text 2>/dev/null || echo "$(YELLOW)No logs available yet$(NC)"; \
		else \
			echo "$(YELLOW)No log stream found yet$(NC)"; \
		fi; \
	else \
		echo "$(YELLOW)No tasks running yet$(NC)"; \
	fi
	@echo ""
	@echo "$(GREEN)To tail logs continuously, run:$(NC)"
	@echo "  $(CYAN)make tail-ecs-logs$(NC)"

update-ecs-service: ## Force new deployment of ECS service
	@echo "$(YELLOW)Forcing new deployment...$(NC)"
	@aws ecs update-service \
		--cluster $(ECS_CLUSTER_NAME) \
		--service $(ECS_SERVICE_NAME) \
		--force-new-deployment \
		--region $(AWS_REGION) > /dev/null
	@echo "$(GREEN)✓ Service updated with new deployment$(NC)"

tail-ecs-logs: ## Tail CloudWatch logs for the ECS service (Ctrl+C to exit)
	@echo "$(YELLOW)Tailing logs from /ecs/$(ECS_TASK_FAMILY)...$(NC)"
	@echo "$(CYAN)Press Ctrl+C to stop$(NC)"
	@echo ""
	@# Use aws logs tail command (requires AWS CLI v2)
	@if aws logs tail --help > /dev/null 2>&1; then \
		aws logs tail /ecs/$(ECS_TASK_FAMILY) \
			--follow \
			--format short \
			--region $(AWS_REGION); \
	else \
		echo "$(YELLOW)AWS CLI v2 'tail' command not available, falling back to polling...$(NC)"; \
		echo ""; \
		LAST_TIMESTAMP=0; \
		while true; do \
			LOG_STREAM=$$(aws logs describe-log-streams \
				--log-group-name /ecs/$(ECS_TASK_FAMILY) \
				--order-by LastEventTime \
				--descending \
				--max-items 1 \
				--region $(AWS_REGION) \
				--query 'logStreams[0].logStreamName' \
				--output text 2>/dev/null); \
			if [ -n "$$LOG_STREAM" ] && [ "$$LOG_STREAM" != "None" ]; then \
				if [ $$LAST_TIMESTAMP -eq 0 ]; then \
					EVENTS=$$(aws logs get-log-events \
						--log-group-name /ecs/$(ECS_TASK_FAMILY) \
						--log-stream-name $$LOG_STREAM \
						--limit 10 \
						--region $(AWS_REGION) 2>/dev/null); \
				else \
					EVENTS=$$(aws logs get-log-events \
						--log-group-name /ecs/$(ECS_TASK_FAMILY) \
						--log-stream-name $$LOG_STREAM \
						--start-time $$LAST_TIMESTAMP \
						--region $(AWS_REGION) 2>/dev/null); \
				fi; \
				if [ -n "$$EVENTS" ]; then \
					echo "$$EVENTS" | jq -r '.events[] | "\(.timestamp | todate) \(.message)"' 2>/dev/null || \
						echo "$$EVENTS" | grep -o '"message":"[^"]*"' | sed 's/"message":"//;s/"$$//'; \
					NEW_TIMESTAMP=$$(echo "$$EVENTS" | jq -r '.events[-1].timestamp // 0' 2>/dev/null); \
					if [ -n "$$NEW_TIMESTAMP" ] && [ "$$NEW_TIMESTAMP" != "0" ] && [ "$$NEW_TIMESTAMP" != "null" ]; then \
						LAST_TIMESTAMP=$$(($$NEW_TIMESTAMP + 1)); \
					fi; \
				fi; \
			fi; \
			sleep 2; \
		done; \
	fi

ecs-status: ## Show current ECS service status and recent logs
	@echo "$(CYAN)Service Status:$(NC)"
	@aws ecs describe-services \
		--cluster $(ECS_CLUSTER_NAME) \
		--services $(ECS_SERVICE_NAME) \
		--region $(AWS_REGION) \
		--query 'services[0].{Status:status,Running:runningCount,Desired:desiredCount,Pending:pendingCount,Events:events[0:3]}' \
		--output table
	@echo ""
	@echo "$(CYAN)Running Tasks:$(NC)"
	@aws ecs list-tasks \
		--cluster $(ECS_CLUSTER_NAME) \
		--service-name $(ECS_SERVICE_NAME) \
		--region $(AWS_REGION) \
		--query 'taskArns[]' \
		--output table
	@echo ""
	@echo "$(CYAN)Recent Logs:$(NC)"
	@LOG_STREAM=$$(aws logs describe-log-streams \
		--log-group-name /ecs/$(ECS_TASK_FAMILY) \
		--order-by LastEventTime \
		--descending \
		--max-items 1 \
		--region $(AWS_REGION) \
		--query 'logStreams[0].logStreamName' \
		--output text 2>/dev/null); \
	if [ -n "$$LOG_STREAM" ] && [ "$$LOG_STREAM" != "None" ]; then \
		aws logs get-log-events \
			--log-group-name /ecs/$(ECS_TASK_FAMILY) \
			--log-stream-name $$LOG_STREAM \
			--limit 20 \
			--region $(AWS_REGION) \
			--query 'events[*].message' \
			--output text 2>/dev/null; \
	else \
		echo "$(YELLOW)No log stream found$(NC)"; \
	fi

get-ecs-url: ## Get the public URL/IP of the ECS service
	@echo "$(YELLOW)Fetching service URL...$(NC)"
	@echo ""
	@# Check if service uses load balancer
	@LB_ARN=$$(aws ecs describe-services \
		--cluster $(ECS_CLUSTER_NAME) \
		--services $(ECS_SERVICE_NAME) \
		--region $(AWS_REGION) \
		--query 'services[0].loadBalancers[0].targetGroupArn' \
		--output text 2>/dev/null); \
	if [ -n "$$LB_ARN" ] && [ "$$LB_ARN" != "None" ]; then \
		echo "$(CYAN)Service is using Application Load Balancer$(NC)"; \
		LB_ARN=$$(aws elbv2 describe-target-groups \
			--target-group-arns $$LB_ARN \
			--region $(AWS_REGION) \
			--query 'TargetGroups[0].LoadBalancerArns[0]' \
			--output text 2>/dev/null); \
		if [ -n "$$LB_ARN" ] && [ "$$LB_ARN" != "None" ]; then \
			LB_DNS=$$(aws elbv2 describe-load-balancers \
				--load-balancer-arns $$LB_ARN \
				--region $(AWS_REGION) \
				--query 'LoadBalancers[0].DNSName' \
				--output text 2>/dev/null); \
			echo ""; \
			echo "$(GREEN)✓ Load Balancer URL: http://$$LB_DNS$(NC)"; \
			echo "$(CYAN)  Health check: http://$$LB_DNS/health$(NC)"; \
		fi; \
	else \
		echo "$(CYAN)Service is using direct public IP (no load balancer)$(NC)"; \
		TASK_ARN=$$(aws ecs list-tasks \
			--cluster $(ECS_CLUSTER_NAME) \
			--service-name $(ECS_SERVICE_NAME) \
			--region $(AWS_REGION) \
			--query 'taskArns[0]' \
			--output text 2>/dev/null); \
		if [ -n "$$TASK_ARN" ] && [ "$$TASK_ARN" != "None" ]; then \
			ENI_ID=$$(aws ecs describe-tasks \
				--cluster $(ECS_CLUSTER_NAME) \
				--tasks $$TASK_ARN \
				--region $(AWS_REGION) \
				--query 'tasks[0].attachments[0].details[?name==`networkInterfaceId`].value' \
				--output text 2>/dev/null); \
			if [ -n "$$ENI_ID" ] && [ "$$ENI_ID" != "None" ]; then \
				PUBLIC_IP=$$(aws ec2 describe-network-interfaces \
					--network-interface-ids $$ENI_ID \
					--region $(AWS_REGION) \
					--query 'NetworkInterfaces[0].Association.PublicIp' \
					--output text 2>/dev/null); \
				if [ -n "$$PUBLIC_IP" ] && [ "$$PUBLIC_IP" != "None" ]; then \
					echo ""; \
					echo "$(GREEN)✓ Service URL: http://$$PUBLIC_IP:$(CONTAINER_PORT)$(NC)"; \
					echo "$(CYAN)  Health check: http://$$PUBLIC_IP:$(CONTAINER_PORT)/health$(NC)"; \
					echo ""; \
					echo "$(YELLOW)Note: Public IP may change if task is restarted. Consider using a load balancer for production.$(NC)"; \
				else \
					echo ""; \
					echo "$(RED)✗ Public IP not assigned$(NC)"; \
					echo "$(YELLOW)The task may still be starting or the service is not in a VPC with public subnets.$(NC)"; \
				fi; \
			else \
				echo ""; \
				echo "$(RED)✗ Network interface not found$(NC)"; \
			fi; \
		else \
			echo ""; \
			echo "$(RED)✗ No running tasks found$(NC)"; \
			echo "$(YELLOW)Check service status with: make ecs-status$(NC)"; \
		fi; \
	fi
	@echo ""

cleanup-ecs: ## Remove ECS service and task definitions
	@echo "$(YELLOW)Cleaning up ECS resources...$(NC)"
	@# Delete service
	@if aws ecs describe-services --cluster $(ECS_CLUSTER_NAME) --services $(ECS_SERVICE_NAME) --region $(AWS_REGION) 2>/dev/null | grep -q "ACTIVE"; then \
		echo "$(YELLOW)Deleting service...$(NC)"; \
		aws ecs update-service \
			--cluster $(ECS_CLUSTER_NAME) \
			--service $(ECS_SERVICE_NAME) \
			--desired-count 0 \
			--region $(AWS_REGION) > /dev/null; \
		aws ecs delete-service \
			--cluster $(ECS_CLUSTER_NAME) \
			--service $(ECS_SERVICE_NAME) \
			--region $(AWS_REGION) > /dev/null; \
		echo "$(GREEN)✓ Service deleted$(NC)"; \
	else \
		echo "$(YELLOW)Service does not exist$(NC)"; \
	fi
	@# Deregister all task definition revisions
	@echo "$(YELLOW)Deregistering task definitions...$(NC)"
	@for arn in $$(aws ecs list-task-definitions --family-prefix $(ECS_TASK_FAMILY) --region $(AWS_REGION) --query 'taskDefinitionArns[]' --output text); do \
		aws ecs deregister-task-definition --task-definition $$arn --region $(AWS_REGION) > /dev/null; \
		echo "$(GREEN)✓ Deregistered: $$arn$(NC)"; \
	done
	@# Delete CloudWatch log group
	@echo "$(YELLOW)Deleting CloudWatch log group...$(NC)"
	@aws logs delete-log-group \
		--log-group-name /ecs/$(ECS_TASK_FAMILY) \
		--region $(AWS_REGION) 2>/dev/null || echo "$(YELLOW)Log group does not exist$(NC)"
	@echo "$(GREEN)✓ Log group deleted$(NC)"
	@# Clean up temp files
	@rm -f /tmp/ecs-subnets.tmp /tmp/ecs-execution-role.tmp /tmp/ecs-task-def.json
	@echo "$(GREEN)✓ Cleanup complete$(NC)"

