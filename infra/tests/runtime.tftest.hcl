mock_provider "azurerm" {
  mock_data "azurerm_client_config" {
    defaults = { tenant_id = "11111111-1111-1111-1111-111111111111" }
  }
}
mock_provider "azapi" {}
mock_provider "random" {}

variables {
  subscription_id       = "11111111-1111-1111-1111-111111111111"
  auth_client_id        = "22222222-2222-2222-2222-222222222222"
  deployer_principal_id = "33333333-3333-3333-3333-333333333333"
}

run "runtime_policy" {
  command = plan
  assert {
    condition     = azurerm_storage_account.app.shared_access_key_enabled == false
    error_message = "Storage must not accept shared keys."
  }
  assert {
    condition     = azurerm_function_app_flex_consumption.app.storage_authentication_type == "SystemAssignedIdentity"
    error_message = "Flex deployment storage must use managed identity."
  }
  assert {
    condition     = azapi_update_resource.go_runtime.body.properties.functionAppConfig.runtime.name == "go"
    error_message = "The Flex Function App must be patched to Azure's native Go runtime."
  }
  assert {
    condition     = azapi_update_resource.go_runtime.body.properties.functionAppConfig.runtime.version == "1.0"
    error_message = "The native Go runtime must use the supported 1.0 worker version."
  }
  assert {
    condition     = !contains(keys(azurerm_function_app_flex_consumption.app.app_settings), "FUNCTIONS_WORKER_RUNTIME")
    error_message = "Flex derives the Go worker from functionAppConfig; do not set FUNCTIONS_WORKER_RUNTIME."
  }
  assert {
    condition     = azurerm_function_app_flex_consumption.app.auth_settings_v2[0].require_authentication == true
    error_message = "The UI must require browser sign-in."
  }
  assert {
    condition     = toset(azurerm_function_app_flex_consumption.app.auth_settings_v2[0].excluded_paths) == toset(["/api/health", "/api/ingest"])
    error_message = "Only ingest and health may bypass Easy Auth."
  }
  assert {
    condition     = azurerm_function_app_flex_consumption.app.app_settings["ALLOW_SIMULATE"] == "false"
    error_message = "Azure simulations must default off."
  }
  assert {
    condition     = length(azapi_resource.container) == 2
    error_message = "Both deployments and outbox storage must exist."
  }
}
