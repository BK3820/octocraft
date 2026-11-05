data "azurerm_client_config" "current" {}

resource "random_id" "suffix" {
  byte_length = 6
}
resource "random_bytes" "ingest" {
  length = 32
}
locals {
  suffix = random_id.suffix.hex
  tags   = { app = "octocraft", managed_by = "terraform" }
}

resource "azurerm_resource_group" "app" {
  name     = var.resource_group_name
  location = var.location
  tags     = local.tags
}
resource "azurerm_storage_account" "app" {
  name                            = "stgaf${local.suffix}"
  resource_group_name             = azurerm_resource_group.app.name
  location                        = var.location
  account_tier                    = "Standard"
  account_replication_type        = "LRS"
  min_tls_version                 = "TLS1_2"
  shared_access_key_enabled       = false
  allow_nested_items_to_be_public = false
  default_to_oauth_authentication = true
  tags                            = local.tags
}

# ARM management-plane creation avoids using account keys or granting the
# provisioning caller persistent data-plane access to application storage.
resource "azapi_resource" "container" {
  for_each  = toset(["deployments", "outbox"])
  type      = "Microsoft.Storage/storageAccounts/blobServices/containers@2023-05-01"
  name      = each.value
  parent_id = "${azurerm_storage_account.app.id}/blobServices/default"
  body      = { properties = { publicAccess = "None" } }
}
resource "azapi_resource" "requests" {
  type      = "Microsoft.Storage/storageAccounts/tableServices/tables@2023-05-01"
  name      = "apprequests"
  parent_id = "${azurerm_storage_account.app.id}/tableServices/default"
  body      = { properties = {} }
}
resource "azurerm_log_analytics_workspace" "app" {
  name                = "log-gaf-${local.suffix}"
  resource_group_name = azurerm_resource_group.app.name
  location            = var.location
  sku                 = "PerGB2018"
  retention_in_days   = 30
  daily_quota_gb      = var.log_daily_cap_gb
  tags                = local.tags
}
resource "azurerm_application_insights" "app" {
  name                         = "appi-gaf-${local.suffix}"
  resource_group_name          = azurerm_resource_group.app.name
  location                     = var.location
  workspace_id                 = azurerm_log_analytics_workspace.app.id
  application_type             = "web"
  local_authentication_enabled = false
  tags                         = local.tags
}
resource "azurerm_key_vault" "app" {
  name                       = "kv-gaf-${local.suffix}"
  resource_group_name        = azurerm_resource_group.app.name
  location                   = var.location
  tenant_id                  = data.azurerm_client_config.current.tenant_id
  sku_name                   = "standard"
  rbac_authorization_enabled = true
  soft_delete_retention_days = 7
  purge_protection_enabled   = false
  tags                       = local.tags
}
resource "azapi_resource" "ingest_secret" {
  type      = "Microsoft.KeyVault/vaults/secrets@2023-07-01"
  name      = "ingest-token"
  parent_id = azurerm_key_vault.app.id
  body      = { properties = { value = random_bytes.ingest.hex } }
}
resource "azurerm_service_plan" "app" {
  name                = "plan-gaf-${local.suffix}"
  resource_group_name = azurerm_resource_group.app.name
  location            = var.location
  os_type             = "Linux"
  sku_name            = "FC1"
  tags                = local.tags
}
resource "azurerm_function_app_flex_consumption" "app" {
  name                        = var.function_app_name
  resource_group_name         = azurerm_resource_group.app.name
  location                    = var.location
  service_plan_id             = azurerm_service_plan.app.id
  storage_container_type      = "blobContainer"
  storage_container_endpoint  = "${azurerm_storage_account.app.primary_blob_endpoint}deployments"
  storage_authentication_type = "SystemAssignedIdentity"
  # AzureRM has not yet added the native Go runtime to its Flex runtime enum.
  # AzAPI below patches this bootstrap value to the supported Go 1.0 runtime.
  runtime_name           = "custom"
  runtime_version        = "1.0"
  maximum_instance_count = 40
  instance_memory_in_mb  = 2048
  https_only             = true
  identity { type = "SystemAssigned" }
  site_config {
    minimum_tls_version                    = "1.2"
    application_insights_connection_string = azurerm_application_insights.app.connection_string
  }
  auth_settings_v2 {
    auth_enabled           = true
    require_authentication = true
    require_https          = true
    unauthenticated_action = "RedirectToLoginPage"
    default_provider       = "azureactivedirectory"
    excluded_paths         = ["/api/ingest", "/api/health"]
    active_directory_v2 {
      client_id            = var.auth_client_id
      tenant_auth_endpoint = "https://login.microsoftonline.com/${data.azurerm_client_config.current.tenant_id}/v2.0"
    }
    login { token_store_enabled = true }
  }
  app_settings = {
    AzureWebJobsStorage__accountName          = azurerm_storage_account.app.name
    AzureWebJobsStorage__credential           = "managedidentity"
    APPLICATIONINSIGHTS_AUTHENTICATION_STRING = "Authorization=AAD"
    STORAGE_ACCOUNT_NAME                      = azurerm_storage_account.app.name
    REQUESTS_TABLE                            = azapi_resource.requests.name
    OUTBOX_CONTAINER                          = "outbox"
    KEY_VAULT_URL                             = azurerm_key_vault.app.vault_uri
    INGEST_TOKEN                              = "@Microsoft.KeyVault(VaultName=${azurerm_key_vault.app.name};SecretName=ingest-token)"
    BASE_URL                                  = "https://${var.function_app_name}.azurewebsites.net"
    ALLOW_SIMULATE                            = tostring(var.allow_simulate)
  }
  depends_on = [azapi_resource.container, azapi_resource.ingest_secret]
  tags       = local.tags
  lifecycle {
    # The AzAPI resource declares Azure's supported Go runtime. Retain the
    # bootstrap value here until AzureRM adds Go to its runtime enum.
    ignore_changes = [runtime_name, runtime_version]
  }
}

resource "azapi_update_resource" "go_runtime" {
  type        = "Microsoft.Web/sites@2024-04-01"
  resource_id = azurerm_function_app_flex_consumption.app.id
  body = {
    properties = {
      functionAppConfig = {
        runtime = {
          name    = "go"
          version = "1.0"
        }
      }
    }
  }
  depends_on = [azurerm_function_app_flex_consumption.app]
}
resource "azurerm_role_assignment" "storage" {
  for_each = toset([
    "Storage Blob Data Owner",
    "Storage Table Data Contributor",
    "Storage Queue Data Contributor",
  ])
  scope                = azurerm_storage_account.app.id
  role_definition_name = each.value
  principal_id         = azurerm_function_app_flex_consumption.app.identity[0].principal_id
}
resource "azurerm_role_assignment" "secrets" {
  scope                = azurerm_key_vault.app.id
  role_definition_name = "Key Vault Secrets Officer"
  principal_id         = azurerm_function_app_flex_consumption.app.identity[0].principal_id
}
resource "azurerm_role_assignment" "metrics" {
  scope                = azurerm_application_insights.app.id
  role_definition_name = "Monitoring Metrics Publisher"
  principal_id         = azurerm_function_app_flex_consumption.app.identity[0].principal_id
}
resource "azurerm_role_assignment" "deployer" {
  scope                = azurerm_function_app_flex_consumption.app.id
  role_definition_name = "Website Contributor"
  principal_id         = var.deployer_principal_id
}
