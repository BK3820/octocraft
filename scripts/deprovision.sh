#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
REPO="${REPO:-StrawHat-Enterprise/github-app-factory}"
if [[ "${1:-}" != "--confirm" || "$#" -ne 1 ]]; then
  echo "Usage: $0 --confirm (Terraform will also ask you to approve destruction)"
  exit 2
fi
gh variable set APP_FACTORY_ENABLED -R "$REPO" -b false
terraform -chdir=infra init
if [[ -z "$(terraform -chdir=infra state list)" ]]; then
  echo "No Terraform-managed resources. Existing unmanaged Azure resources must be imported first."
  exit 0
fi
terraform -chdir=infra destroy
echo "Terraform-managed Azure runtime deleted; Entra registrations and GitHub configuration retained."
