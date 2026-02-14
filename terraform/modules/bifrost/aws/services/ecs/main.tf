# =============================================================================
# ECS Fargate Service Module
# =============================================================================

# --- ECS Cluster (optional) ---

resource "aws_ecs_cluster" "this" {
  count = var.create_cluster ? 1 : 0

  name = "${var.name_prefix}-cluster"

  setting {
    name  = "containerInsights"
    value = "enabled"
  }

  tags = merge(var.tags, {
    Name = "${var.name_prefix}-cluster"
  })
}

data "aws_caller_identity" "current" {}

locals {
  cluster_arn  = var.create_cluster ? aws_ecs_cluster.this[0].arn : "arn:aws:ecs:${var.region}:${data.aws_caller_identity.current.account_id}:cluster/${var.name_prefix}-cluster"
  cluster_name = var.create_cluster ? aws_ecs_cluster.this[0].name : "${var.name_prefix}-cluster"
}

# --- ECS Task Definition ---

resource "aws_ecs_task_definition" "bifrost" {
  family                   = "${var.name_prefix}-task"
  network_mode             = "awsvpc"
  requires_compatibilities = ["FARGATE"]
  cpu                      = tostring(var.cpu)
  memory                   = tostring(var.memory)
  execution_role_arn       = var.execution_role_arn

  container_definitions = jsonencode([
    {
      name      = "bifrost"
      image     = var.image
      essential = true

      entryPoint = ["/bin/sh", "-c"]
      command = [
        "if [ -n \"$BIFROST_CONFIG\" ]; then printf '%s' \"$BIFROST_CONFIG\" > /app/data/config.json; else echo \"ERROR: BIFROST_CONFIG not set\" >&2 && exit 1; fi && exec /app/docker-entrypoint.sh /app/main"
      ]

      portMappings = [
        {
          containerPort = var.container_port
          protocol      = "tcp"
        }
      ]

      secrets = [
        {
          name      = "BIFROST_CONFIG"
          valueFrom = var.secret_arn
        }
      ]

      healthCheck = {
        command     = ["CMD-SHELL", "wget --no-verbose --tries=1 --spider http://localhost:${var.container_port}${var.health_check_path} || exit 1"]
        interval    = 30
        timeout     = 5
        retries     = 3
        startPeriod = 60
      }

      logConfiguration = {
        logDriver = "awslogs"
        options = {
          "awslogs-group"         = var.log_group_name
          "awslogs-region"        = var.region
          "awslogs-stream-prefix" = "bifrost"
        }
      }
    }
  ])

  tags = merge(var.tags, {
    Name = "${var.name_prefix}-task"
  })
}

# --- ECS Service ---

resource "aws_ecs_service" "bifrost" {
  name            = "${var.name_prefix}-service"
  cluster         = local.cluster_arn
  task_definition = aws_ecs_task_definition.bifrost.arn
  desired_count   = var.desired_count
  launch_type     = "FARGATE"

  network_configuration {
    subnets          = var.subnet_ids
    security_groups  = var.security_group_ids
    assign_public_ip = var.assign_public_ip
  }

  dynamic "load_balancer" {
    for_each = var.create_load_balancer ? [1] : []
    content {
      target_group_arn = aws_lb_target_group.bifrost[0].arn
      container_name   = "bifrost"
      container_port   = var.container_port
    }
  }

  health_check_grace_period_seconds = var.create_load_balancer ? 60 : null

  depends_on = [
    aws_ecs_task_definition.bifrost,
  ]

  tags = merge(var.tags, {
    Name = "${var.name_prefix}-service"
  })
}

# =============================================================================
# Application Load Balancer (optional)
# =============================================================================

resource "aws_lb" "bifrost" {
  count = var.create_load_balancer ? 1 : 0

  name               = "${var.name_prefix}-alb"
  internal           = false
  load_balancer_type = "application"
  security_groups    = var.security_group_ids
  subnets            = var.subnet_ids

  tags = merge(var.tags, {
    Name = "${var.name_prefix}-alb"
  })
}

resource "aws_lb_target_group" "bifrost" {
  count = var.create_load_balancer ? 1 : 0

  name        = "${var.name_prefix}-tg"
  port        = var.container_port
  protocol    = "HTTP"
  vpc_id      = var.vpc_id
  target_type = "ip"

  health_check {
    enabled             = true
    path                = var.health_check_path
    port                = "traffic-port"
    protocol            = "HTTP"
    healthy_threshold   = 3
    unhealthy_threshold = 3
    timeout             = 5
    interval            = 30
    matcher             = "200"
  }

  tags = merge(var.tags, {
    Name = "${var.name_prefix}-tg"
  })
}

resource "aws_lb_listener" "http" {
  count = var.create_load_balancer ? 1 : 0

  load_balancer_arn = aws_lb.bifrost[0].arn
  port              = 80
  protocol          = "HTTP"

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.bifrost[0].arn
  }

  tags = var.tags
}

# =============================================================================
# Auto Scaling (optional)
# =============================================================================

resource "aws_appautoscaling_target" "bifrost" {
  count = var.enable_autoscaling ? 1 : 0

  max_capacity       = var.max_capacity
  min_capacity       = var.min_capacity
  resource_id        = "service/${local.cluster_name}/${aws_ecs_service.bifrost.name}"
  scalable_dimension = "ecs:service:DesiredCount"
  service_namespace  = "ecs"
}

resource "aws_appautoscaling_policy" "cpu" {
  count = var.enable_autoscaling ? 1 : 0

  name               = "${var.name_prefix}-cpu-scaling"
  policy_type        = "TargetTrackingScaling"
  resource_id        = aws_appautoscaling_target.bifrost[0].resource_id
  scalable_dimension = aws_appautoscaling_target.bifrost[0].scalable_dimension
  service_namespace  = aws_appautoscaling_target.bifrost[0].service_namespace

  target_tracking_scaling_policy_configuration {
    target_value = var.autoscaling_cpu_threshold

    predefined_metric_specification {
      predefined_metric_type = "ECSServiceAverageCPUUtilization"
    }

    scale_in_cooldown  = 300
    scale_out_cooldown = 60
  }
}

resource "aws_appautoscaling_policy" "memory" {
  count = var.enable_autoscaling ? 1 : 0

  name               = "${var.name_prefix}-memory-scaling"
  policy_type        = "TargetTrackingScaling"
  resource_id        = aws_appautoscaling_target.bifrost[0].resource_id
  scalable_dimension = aws_appautoscaling_target.bifrost[0].scalable_dimension
  service_namespace  = aws_appautoscaling_target.bifrost[0].service_namespace

  target_tracking_scaling_policy_configuration {
    target_value = var.autoscaling_memory_threshold

    predefined_metric_specification {
      predefined_metric_type = "ECSServiceAverageMemoryUtilization"
    }

    scale_in_cooldown  = 300
    scale_out_cooldown = 60
  }
}
