output "service_url" {
  description = "URL to access the Bifrost service."
  value = try(coalesce(
    try(module.aks[0].service_url, null),
    try(module.aci[0].service_url, null),
  ), null)
}

output "health_check_url" {
  description = "URL to the Bifrost health check endpoint."
  value = try(coalesce(
    try(module.aks[0].health_check_url, null),
    try(module.aci[0].health_check_url, null),
  ), null)
}
