# Bifrost on AWS ECS with RDS PostgreSQL

The one-click AWS stack: an ECS Fargate service, an RDS PostgreSQL 16 instance for both Bifrost stores, generated runtime secrets in Secrets Manager, and an Application Load Balancer.

The fastest path is the deploy script from AWS CloudShell:

```bash
git clone --depth 1 https://github.com/maximhq/bifrost && bifrost/deploy/aws/deploy.sh
```

## Prerequisites

- AWS credentials with permission to create VPC, ECS, RDS, IAM, Secrets Manager, CloudWatch Logs and ALB resources
- Terraform >= 1.7 (the deploy script installs it on Amazon Linux)

## Manual usage

```bash
cp terraform.tfvars.example terraform.tfvars
terraform init
terraform apply
terraform output -raw setup_token; echo
```

Open the `service_url` output, create the first admin account with the setup token, then add providers and a virtual key.

## What gets created

| Resource | Purpose |
|---|---|
| VPC, two public subnets, security groups | Network for the service and the database |
| ECS cluster, task definition, Fargate service | Runs the Bifrost image |
| RDS PostgreSQL 16 (`db.t4g.small`, 20 GB gp3, encrypted, 7-day backups) | Config store and log store |
| Secrets Manager `<prefix>/config` and `<prefix>/runtime` | `config.json`, database password, encryption key, setup token |
| Application Load Balancer | Public HTTP entry point on port 80 |

The ALB listener is HTTP. Put a certificate in front of it (ACM plus an HTTPS listener, or your own proxy) before sending production traffic.

## Enterprise

Set `image_repository` to the registry path Maxim issued for your organization. Private registries need `repositoryCredentials` on the task definition or ECR pull permissions on the execution role; see the enterprise deployment guide.

## Cleanup

```bash
terraform destroy
```

With `database_deletion_protection = true` the database refuses deletion until the flag is flipped back and applied. Set `database_skip_final_snapshot = false` before that destroy to keep a final snapshot named `<name_prefix>-db-final`; the two settings are independent.
