variable "subscription_id" {
  description = "Azure subscription selected for the deployment."
  type        = string
}
variable "resource_group_name" {
  type    = string
  default = "rg-github-app-factory"
}
variable "location" {
  type    = string
  default = "eastus2"
}
variable "function_app_name" {
  type    = string
  default = "strawhat-ghapp-factory"
  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{1,30}[a-z0-9]$", var.function_app_name))
    error_message = "Choose a globally unique lowercase Function App name of 3-32 characters."
  }
}
variable "auth_client_id" {
  description = "Existing Entra application client ID for browser sign-in; authentication is mandatory."
  type        = string
}
variable "deployer_principal_id" {
  description = "Existing OIDC deployer service principal object ID."
  type        = string
}
variable "allow_simulate" {
  type    = bool
  default = false
}
variable "log_daily_cap_gb" {
  description = "Ingestion cap, not a guaranteed hard budget limit; delayed enforcement can exceed it."
  type        = number
  default     = 0.1
}
