#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

REPO="${REPO:-StrawHat-Enterprise/octocraft}"
RG="${RG:-rg-octocraft}"
LOCATION="${LOCATION:-eastus2}"
APP_NAME="${APP_NAME:-strawhat-octocraft}"
export REPO

gh variable set APP_FACTORY_ENABLED -R "$REPO" -b false
# Fail before creating Azure resources if the private-repo plan cannot enforce approvals.
node scripts/configure-github.js

ensure_app() {
  local display_name="$1"
  local legacy_display_name="${2:-}"
  local id
  id=$(az ad app list --display-name "$display_name" --query "[0].appId" -o tsv)
  if [[ -z "$id" && -n "$legacy_display_name" ]]; then
    id=$(az ad app list --display-name "$legacy_display_name" --query "[0].appId" -o tsv)
    if [[ -n "$id" ]]; then
      az ad app update --id "$id" --display-name "$display_name"
    fi
  fi
  if [[ -z "$id" ]]; then
    id=$(az ad app create --display-name "$display_name" --sign-in-audience AzureADMyOrg --query appId -o tsv)
    az ad sp create --id "$id" >/dev/null
  fi
  echo "$id"
}

TENANT_ID=$(az account show --query tenantId -o tsv)
SUB_ID=$(az account show --query id -o tsv)
# Rename the retained registrations in place, preserving their app and service-principal IDs.
UI_APP_ID=$(ensure_app octocraft-ui github-app-factory-ui)
DEPLOY_APP_ID=$(ensure_app octocraft-deployer github-app-factory-deployer)
DEPLOY_SP_ID=$(az ad sp show --id "$DEPLOY_APP_ID" --query id -o tsv)
az ad app update --id "$UI_APP_ID" \
  --web-redirect-uris "https://${APP_NAME}.azurewebsites.net/.auth/login/aad/callback" \
  --enable-id-token-issuance true --identifier-uris "api://${UI_APP_ID}"

OWNER_ID=$(gh api "repos/${REPO}" --jq .owner.id)
REPO_ID=$(gh api "repos/${REPO}" --jq .id)
SUBJECT="repo:${REPO%%/*}@${OWNER_ID}/${REPO##*/}@${REPO_ID}:environment:production"
for old in $(az ad app federated-credential list --id "$DEPLOY_APP_ID" --query "[?subject!='${SUBJECT}'].id" -o tsv); do
  az ad app federated-credential delete --id "$DEPLOY_APP_ID" --federated-credential-id "$old"
done
if [[ -z "$(az ad app federated-credential list --id "$DEPLOY_APP_ID" --query "[?subject=='${SUBJECT}'].id" -o tsv)" ]]; then
  az ad app federated-credential create --id "$DEPLOY_APP_ID" --parameters "{
    \"name\":\"github-production\", \"issuer\":\"https://token.actions.githubusercontent.com\",
    \"subject\":\"${SUBJECT}\", \"audiences\":[\"api://AzureADTokenExchange\"]
  }" >/dev/null
fi

export TF_VAR_subscription_id="$SUB_ID"
export TF_VAR_auth_client_id="$UI_APP_ID"
export TF_VAR_deployer_principal_id="$DEPLOY_SP_ID"
export TF_VAR_resource_group_name="$RG"
export TF_VAR_location="$LOCATION"
export TF_VAR_function_app_name="$APP_NAME"
terraform -chdir=infra init
terraform -chdir=infra fmt -check
terraform -chdir=infra validate
# Terraform asks for confirmation. No resources are created without that approval.
terraform -chdir=infra apply

gh variable set AZURE_CLIENT_ID -R "$REPO" -b "$DEPLOY_APP_ID"
gh variable set AZURE_TENANT_ID -R "$REPO" -b "$TENANT_ID"
gh variable set AZURE_SUBSCRIPTION_ID -R "$REPO" -b "$SUB_ID"
gh variable set AZURE_FUNCTIONAPP_NAME -R "$REPO" -b "$(terraform -chdir=infra output -raw function_app_name)"
gh variable set APP_FACTORY_URL -R "$REPO" -b "$(terraform -chdir=infra output -raw url)"
terraform -chdir=infra output -raw ingest_token | gh secret set APP_FACTORY_INGEST_TOKEN -R "$REPO"
gh label create github-app-request -R "$REPO" -c 0E8A16 -d "GitHub App request" --force
gh label create awaiting-approval -R "$REPO" -c FBCA04 -d "Waiting for app approval" --force

echo "Provisioned. APP_FACTORY_ENABLED remains false."
echo "Review production protection, then set APP_FACTORY_ENABLED=true and dispatch Build and deploy."
echo "The production job will wait for BK3820 approval before Azure login."
