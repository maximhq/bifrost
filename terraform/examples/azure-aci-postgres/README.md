# Bifrost on Azure Container Instances with PostgreSQL Flexible Server

The one-click Azure stack: a public container group, an Azure Database for PostgreSQL Flexible Server (PostgreSQL 16) for both Bifrost stores, and generated runtime secrets delivered as secure environment variables.

The fastest path is the deploy script from Azure Cloud Shell:

```bash
git clone --depth 1 https://github.com/maximhq/bifrost && bifrost/deploy/azure/deploy.sh
```

## Prerequisites

- An Azure subscription where you can create resource groups, Container Instances, Flexible Servers, Key Vaults and managed identities (Contributor is enough)
- Terraform >= 1.7 (preinstalled in Cloud Shell)

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
| Resource group, VNet, subnet, network security group | Network scaffold shared with the AKS target |
| Key Vault and user-assigned identity | Holds `config.json`; identity for the container group |
| Flexible Server PostgreSQL 16 (`B_Standard_B1ms`, 32 GB, 7-day backups, TLS required) | Config store and log store |
| Container group with a public IP and DNS label | Runs the Bifrost image on port 8080 |

The database has a public endpoint whose firewall rule is Azure's allow-Azure-services marker (`0.0.0.0`): it admits any Azure-hosted source, including other tenants, so the generated password and the enforced TLS are the only access controls. That rule is what lets the container group connect. The container group answers plain HTTP on `http://<prefix>.<region>.azurecontainer.io:8080`; put Azure Front Door, Application Gateway, or your own proxy in front for TLS before production traffic.

Key Vault is created with `default_action = "Allow"` on its network ACL so Cloud Shell can write the config secret; access still requires the access policy. Switch it back to `Deny` and add your network once you deploy from inside Azure.

## Enterprise

Set `image_repository` to the registry path Maxim issued for your organization and `registry_server`, `registry_username` and `registry_password` to the pull credential; they become an `image_registry_credential` block on the container group. Container Instances cannot pull a private image with a managed identity alone, so the credential is required. The one-click script keeps an existing `terraform.tfvars`, so add these three lines there and run `terraform apply`.

## Cleanup

```bash
terraform destroy
```

With `database_deletion_protection = true` a `CanNotDelete` lock refuses the destroy until the flag is flipped back and applied.
