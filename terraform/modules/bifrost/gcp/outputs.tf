output "service_url" {
  description = "URL to access the Bifrost service."
  value = coalesce(
    try(module.gke[0].service_url, null),
    try(module.cloud_run[0].service_url, null),
  )
}

output "health_check_url" {
  description = "URL to the Bifrost health check endpoint."
  value = coalesce(
    try(module.gke[0].health_check_url, null),
    try(module.cloud_run[0].health_check_url, null),
  )
}
