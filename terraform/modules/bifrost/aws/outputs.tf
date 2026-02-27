output "service_url" {
  description = "URL to access the Bifrost service."
  value = try(
    module.ecs[0].service_url,
    module.eks[0].service_url,
    null,
  )
}

output "health_check_url" {
  description = "URL to the Bifrost health check endpoint."
  value = try(
    module.ecs[0].health_check_url,
    module.eks[0].health_check_url,
    null,
  )
}
