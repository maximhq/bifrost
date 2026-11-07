output "service_url" {
  value = module.bifrost.service_url
}

output "health_check_url" {
  value = module.bifrost.health_check_url
}

output "config_json" {
  value     = module.bifrost.config_json
  sensitive = true
}

output "setup_token" {
  value     = module.bifrost.setup_token
  sensitive = true
}

output "database_endpoint" {
  value = module.bifrost.database_endpoint
}
