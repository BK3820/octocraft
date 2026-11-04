#!/usr/bin/env bash
# Removes the deployed Azure runtime while preserving Entra app registrations
# and GitHub OIDC configuration for a later reprovision.
set -euo pipefail
cd "$(dirname "$0")/.."

REPO="${REPO:-StrawHat-Enterprise/github-app-factory}"
RG="${RG:-rg-github-app-factory}"
VAULTS=()

if [[ "${1:-}" != "--confirm" || "$#" -ne 1 ]]; then
  echo "Usage: $0 --confirm"
  echo "This deletes every Azure resource in resource group: $RG"
  exit 2
fi

echo "== Disabling remote deployment and IssueOps ingestion"
gh variable set APP_FACTORY_ENABLED -R "$REPO" -b false

if [[ "$(az group exists --name "$RG")" != "true" ]]; then
  echo "Resource group $RG does not exist; nothing to delete."
  exit 0
fi

# Key Vault uses soft delete. Save the vault names before deleting the group so
# the names and their secret data can be purged after the group is gone.
while IFS=$'\t' read -r name location; do
  [[ -n "$name" ]] && VAULTS+=("${name}|${location}")
done < <(az keyvault list --resource-group "$RG" --query '[].[name,location]' --output tsv)

echo "== Deleting resource group $RG"
az group delete --name "$RG" --yes --no-wait

printf "Waiting for Azure resource deletion"
until [[ "$(az group exists --name "$RG")" == "false" ]]; do
  printf "."
  sleep 15
done
echo

for vault in "${VAULTS[@]}"; do
  name="${vault%%|*}"
  location="${vault#*|}"
  echo "== Purging soft-deleted Key Vault $name"
  for _ in {1..12}; do
    if az keyvault purge --name "$name" --location "$location" >/dev/null 2>&1; then
      break
    fi
    sleep 5
  done
  if az keyvault show-deleted --name "$name" >/dev/null 2>&1; then
    echo "Could not purge Key Vault $name." >&2
    exit 1
  fi
done

echo "Deleted $RG."
echo "Retained Entra registrations and GitHub settings for a later ./scripts/provision.sh run."
