#!/usr/bin/env bash
# One-time (idempotent) provisioning for GitHub App Factory:
#   1. Entra app registration for UI sign-in (Easy Auth)
#   2. Entra app registration + federated credential for GitHub Actions OIDC deploys
#   3. Azure resources via infra/main.bicep
#   4. GitHub repo variables, secrets and IssueOps labels
# Requires: az (logged in, Owner on the subscription), gh (repo admin).
set -euo pipefail
cd "$(dirname "$0")/.."

REPO="${REPO:-StrawHat-Enterprise/github-app-factory}"
RG="${RG:-rg-github-app-factory}"
LOCATION="${LOCATION:-eastus2}"
APP_NAME="${APP_NAME:-strawhat-ghapp-factory}"
URL="https://${APP_NAME}.azurewebsites.net"

TENANT_ID=$(az account show --query tenantId -o tsv)
SUB_ID=$(az account show --query id -o tsv)
echo "Subscription $SUB_ID | Tenant $TENANT_ID | RG $RG | App $APP_NAME"

# Do not let a push deploy until the Azure resources and repo configuration are complete.
gh variable set APP_FACTORY_ENABLED -R "$REPO" -b false

ensure_app() { # name -> appId
  local id
  id=$(az ad app list --display-name "$1" --query "[0].appId" -o tsv)
  [ -z "$id" ] && id=$(az ad app create --display-name "$1" --sign-in-audience AzureADMyOrg --query appId -o tsv)
  echo "$id"
}

echo "== Entra: UI sign-in app"
UI_APP_ID=$(ensure_app "github-app-factory-ui")
az ad app update --id "$UI_APP_ID" \
  --web-redirect-uris "${URL}/.auth/login/aad/callback" \
  --enable-id-token-issuance true \
  --identifier-uris "api://${UI_APP_ID}" >/dev/null
az ad sp show --id "$UI_APP_ID" >/dev/null 2>&1 || az ad sp create --id "$UI_APP_ID" >/dev/null

echo "== Entra: GitHub Actions deployer (OIDC)"
DEPLOY_APP_ID=$(ensure_app "github-app-factory-deployer")
az ad sp show --id "$DEPLOY_APP_ID" >/dev/null 2>&1 || az ad sp create --id "$DEPLOY_APP_ID" >/dev/null
DEPLOY_SP_ID=$(az ad sp show --id "$DEPLOY_APP_ID" --query id -o tsv)
# GitHub now issues immutable-ID subject claims: repo:<owner>@<owner_id>/<repo>@<repo_id>:ref:...
OWNER_ID=$(gh api "repos/${REPO}" --jq .owner.id)
REPO_ID=$(gh api "repos/${REPO}" --jq .id)
SUBJECT="repo:${REPO%%/*}@${OWNER_ID}/${REPO##*/}@${REPO_ID}:ref:refs/heads/main"
for old in $(az ad app federated-credential list --id "$DEPLOY_APP_ID" --query "[?subject!='${SUBJECT}'].id" -o tsv); do
  az ad app federated-credential delete --id "$DEPLOY_APP_ID" --federated-credential-id "$old"
done
if ! az ad app federated-credential list --id "$DEPLOY_APP_ID" --query "[?subject=='${SUBJECT}']" -o tsv | grep -q .; then
  az ad app federated-credential create --id "$DEPLOY_APP_ID" --parameters "{
    \"name\": \"github-main\",
    \"issuer\": \"https://token.actions.githubusercontent.com\",
    \"subject\": \"${SUBJECT}\",
    \"audiences\": [\"api://AzureADTokenExchange\"]
  }" >/dev/null
fi

echo "== Azure: resource group + Bicep"
az group create -n "$RG" -l "$LOCATION" --tags app=github-app-factory -o none

# Reuse the existing ingest token if the Key Vault already has one.
KV=$(az keyvault list -g "$RG" --query "[0].name" -o tsv 2>/dev/null || true)
INGEST_TOKEN=""
[ -n "$KV" ] && INGEST_TOKEN=$(az keyvault secret show --vault-name "$KV" -n ingest-token --query value -o tsv 2>/dev/null || true)
[ -z "$INGEST_TOKEN" ] && INGEST_TOKEN=$(openssl rand -hex 32)

az deployment group create -g "$RG" -n github-app-factory -f infra/main.bicep \
  -p functionAppName="$APP_NAME" ingestToken="$INGEST_TOKEN" \
     authClientId="$UI_APP_ID" deployerPrincipalId="$DEPLOY_SP_ID" \
  --query properties.outputs -o json

echo "== GitHub: variables, secrets, labels"
gh variable set AZURE_CLIENT_ID       -R "$REPO" -b "$DEPLOY_APP_ID"
gh variable set AZURE_TENANT_ID       -R "$REPO" -b "$TENANT_ID"
gh variable set AZURE_SUBSCRIPTION_ID -R "$REPO" -b "$SUB_ID"
gh variable set AZURE_FUNCTIONAPP_NAME -R "$REPO" -b "$APP_NAME"
gh variable set APP_FACTORY_URL       -R "$REPO" -b "$URL"
printf '%s' "$INGEST_TOKEN" | gh secret set APP_FACTORY_INGEST_TOKEN -R "$REPO"
gh label create github-app-request -R "$REPO" -c 0E8A16 -d "GitHub App creation request (IssueOps)" --force
gh label create awaiting-approval  -R "$REPO" -c FBCA04 -d "Queued in GitHub App Factory, waiting for approver" --force
gh variable set APP_FACTORY_ENABLED -R "$REPO" -b true

echo "Done → $URL"
