output "service_account_name" {
  description = "The name of the service account created for VPA"
  value       = try(module.vpa.service_account_name, "")
}

output "metadata" {
  value       = module.vpa.metadata
  description = "Block status of the deployed release"
}
