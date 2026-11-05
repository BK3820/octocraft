#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHART="$ROOT/charts/octocraft"
TMP="$(mktemp -d)"
trap 'rm -f "$TMP/aks.yaml" "$TMP/existing.yaml" "$TMP/custom.yaml" "$TMP/error.log" "$TMP/released.yaml" "$TMP/octocraft-1.2.3.tgz"; rmdir "$TMP"' EXIT

helm lint --strict "$CHART" -f "$CHART/values-aks.example.yaml"
helm template octocraft "$CHART" --namespace octocraft -f "$CHART/values-aks.example.yaml" > "$TMP/aks.yaml"
helm template octocraft "$CHART" --namespace octocraft -f "$CHART/values-aks.example.yaml" --set persistence.existingClaim=retained-data > "$TMP/existing.yaml"
helm template octocraft "$CHART" --namespace octocraft \
  -f "$CHART/values-aks.example.yaml" \
  --set image.repository=example.azurecr.io/octocraft,image.tag=commit-sha \
  --set 'imagePullSecrets[0].name=registry-credentials' \
  --set secrets.existingSecret=custom-secret,secrets.githubTokenKey=github-token \
  --set persistence.storageClass=managed-csi \
  --set app.allowSimulate=true > "$TMP/custom.yaml"

expect_line() {
  if ! grep -Fq "$2" "$1"; then
    echo "Missing expected rendered value: $2" >&2
    exit 1
  fi
}

reject_line() {
  if grep -Fq "$2" "$1"; then
    echo "Unexpected rendered value: $2" >&2
    exit 1
  fi
}

for profile in "$TMP/aks.yaml" "$TMP/custom.yaml"; do
  expect_line "$profile" 'kind: Deployment'
  expect_line "$profile" 'kind: ServiceAccount'
  expect_line "$profile" 'type: ClusterIP'
  expect_line "$profile" 'replicas: 1'
  expect_line "$profile" 'type: Recreate'
  expect_line "$profile" 'readOnlyRootFilesystem: true'
  expect_line "$profile" 'runAsNonRoot: true'
  expect_line "$profile" 'optional: false'
  expect_line "$profile" 'path: /api/health'
  expect_line "$profile" 'kind: PersistentVolumeClaim'
  expect_line "$profile" 'helm.sh/resource-policy: keep'
  expect_line "$profile" 'azure.workload.identity/use: "true"'
  expect_line "$profile" 'azure.workload.identity/client-id:'
  expect_line "$profile" 'azure.workload.identity/tenant-id:'
  expect_line "$profile" 'name: KEY_VAULT_URL'
  expect_line "$profile" 'name: VAULT_ONLY'
  expect_line "$profile" 'name: INGEST_TOKEN_FILE'
  expect_line "$profile" 'mountPath: /data'
  expect_line "$profile" 'mountPath: /var/run/factory-secrets'
  expect_line "$profile" 'readOnly: true'
  expect_line "$profile" 'defaultMode: 0440'
  reject_line "$profile" 'name: STORAGE_ACCOUNT_NAME'
  reject_line "$profile" 'name: REQUESTS_TABLE'
  reject_line "$profile" 'name: OUTBOX_CONTAINER'
  reject_line "$profile" 'secretKeyRef:'
  reject_line "$profile" 'kind: Ingress'
  reject_line "$profile" 'kind: Secret'
done

reject_line "$TMP/existing.yaml" 'kind: PersistentVolumeClaim'
expect_line "$TMP/existing.yaml" 'claimName: "retained-data"'
expect_line "$TMP/custom.yaml" 'image: "example.azurecr.io/octocraft:commit-sha"'
expect_line "$TMP/aks.yaml" 'image: "ghcr.io/strawhat-enterprise/octocraft:0.2.0"'
helm package "$CHART" --version 1.2.3 --app-version 1.2.3 --destination "$TMP" > /dev/null
helm template octocraft "$TMP/octocraft-1.2.3.tgz" --namespace octocraft -f "$CHART/values-aks.example.yaml" > "$TMP/released.yaml"
expect_line "$TMP/released.yaml" 'image: "ghcr.io/strawhat-enterprise/octocraft:1.2.3"'
expect_line "$TMP/aks.yaml" 'name: octocraft-octocraft'
expect_line "$TMP/custom.yaml" 'name: registry-credentials'
expect_line "$TMP/custom.yaml" 'secretName: "custom-secret"'
expect_line "$TMP/custom.yaml" 'name: GITHUB_TOKEN_FILE'
expect_line "$TMP/custom.yaml" 'key: "github-token"'
expect_line "$TMP/custom.yaml" 'storageClassName: "managed-csi"'

expect_failure() {
  local expected="$1"
  shift
  if helm template octocraft "$CHART" --namespace octocraft -f "$CHART/values-aks.example.yaml" "$@" > "$TMP/error.log" 2>&1; then
    echo "Invalid configuration unexpectedly rendered: $*" >&2
    exit 1
  fi
  expect_line "$TMP/error.log" "$expected"
}

expect_failure 'replicaCount' --set replicaCount=2
expect_failure 'existingSecret' --set-string secrets.existingSecret=
expect_failure 'ingestTokenKey' --set-string secrets.ingestTokenKey=
expect_failure 'service' --set service.type=LoadBalancer
expect_failure 'backend' --set backend=azure
expect_failure 'azure' --set azure.storageAccountName=forbiddenaccount
expect_failure 'keyVault.url is required' --set-string keyVault.url=
expect_failure 'Azure Workload Identity requires clientId and tenantId' --set-string workloadIdentity.clientId=
expect_failure 'Azure Workload Identity requires clientId and tenantId' --set-string workloadIdentity.tenantId=
expect_failure 'enabled' --set workloadIdentity.enabled=false
if helm template octocraft "$CHART" --namespace octocraft > "$TMP/error.log" 2>&1; then
  echo "Unconfigured Key Vault defaults unexpectedly rendered" >&2
  exit 1
fi
expect_line "$TMP/error.log" 'keyVault.url is required'

echo "PVC-only storage, Key Vault delivery, and Helm configuration guards passed."
