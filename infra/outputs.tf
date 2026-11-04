output "url" {
  value = "https://${azurerm_function_app_flex_consumption.app.name}.azurewebsites.net"
}
output "function_app_name" {
  value = azurerm_function_app_flex_consumption.app.name
}
output "key_vault_name" {
  value = azurerm_key_vault.app.name
}
output "ingest_token" {
  value     = random_bytes.ingest.hex
  sensitive = true
}
