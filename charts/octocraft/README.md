# OctoCraft: PVC + Key Vault Helm chart

This chart runs the standalone Go server from [cmd/server/main.go](../../cmd/server/main.go) with **filesystem storage on a PVC and generated credentials in Azure Key Vault**. It never configures Azure Table, Blob, or a Storage Account.

**Status:** locally validated, not installed on AKS. The separate Azure Functions Terraform/pipeline remains unchanged and still needs a Storage Account; it is not the Kubernetes deployment.

## Data and secret flow

```text
GitHub issue -> IssueOps -> Go pod -> /data/requests.json on PVC
                                      |
Browser approval -> GitHub conversion -> credentials in pod memory
                                      |
AKS projected identity token -> Entra access token -> Key Vault secret writes
                                      |
PVC request record stores app metadata and Vault references, not credentials
```

The pod uses Go's `DefaultAzureCredential` with AKS Workload Identity. The webhook injects a short-lived projected identity token and identity configuration; no Azure client password or Key Vault credential is stored in the image, PVC, or Helm values. SDK writes go directly to Key Vault over HTTPS, with no mock-vault fallback.

`VAULT_ONLY=true` is enforced by the chart. It requires a Key Vault URL and a nonempty ingestion token at startup. Email/ZIP delivery is rejected at ingestion, approval, callback and finalization; old outbox downloads are blocked. The UI hides ZIP controls. Local development and Functions retain their existing behavior unless that flag is explicitly enabled.

Generated credentials necessarily exist temporarily in process memory. This is not a guarantee of memory zeroization or protection against a compromised pod. Keep images patched, isolate workloads, and tightly scope the identity's permissions.

## Kubernetes resources

| Resource / setting | Purpose |
|---|---|
| Deployment | One non-root Go container, read-only root filesystem, dropped capabilities, resource requests/limits |
| Recreate strategy | Avoids concurrent file writers; upgrades have downtime |
| ClusterIP Service | Internal port 80 to container port 3000; no public IP or ingress |
| ServiceAccount | Workload Identity annotations; normal Kubernetes API token automount disabled |
| PVC | ReadWriteOnce data at `/data`; chart-created claims retained on uninstall |
| Existing Secret volume | Ingestion and optional GitHub tokens mounted read-only at `/var/run/factory-secrets`, not injected as secret-value environment variables |
| Health probes | `/api/health` tests HTTP responsiveness, not Key Vault connectivity or permission |

Only one replica is supported: filesystem locks are process-local. A shared filesystem or multiple PVC mounts do not make concurrent request updates safe.

## Prerequisites: no Storage Account

1. An existing AKS cluster with OIDC issuer and Azure Workload Identity enabled.
2. Pull access to the released image `ghcr.io/strawhat-enterprise/octocraft:<chart appVersion>`. The package is private, so add an `imagePullSecrets` entry or mirror the image to ACR. This is separate from the app's identity. An empty `image.tag` resolves to the chart `appVersion`.
3. A dynamically provisioned Azure **managed disk** PVC, for example StorageClass `managed-csi`, or an existing compatible writable claim. Avoid Azure Files classes: those are backed by a Storage Account. This chart does not provision a cloud account.
4. An Azure Key Vault with RBAC enabled and a dedicated user-assigned app identity. Grant `Key Vault Secrets Officer` scoped to this app's vault; no storage data-plane roles are needed. The app writes generated private-key, client-secret and optional webhook-secret values.
5. A federated identity credential using the AKS OIDC issuer, audience `api://AzureADTokenExchange`, and subject **`system:serviceaccount:octocraft:octocraft-octocraft`** for the example namespace/release. Changing either name changes that subject.
6. Network access from the pod to GitHub, Entra and Key Vault.

Helm does not build images, provision AKS/Key Vault, create federation, or assign Azure roles.

## Configure and install

Copy [values-aks.example.yaml](values-aks.example.yaml) to a private operator configuration. Replace its Key Vault URL, identity client ID and tenant ID (the image defaults to the release matching the chart version). These IDs are configuration, not client passwords.

Defaults intentionally leave the real Vault/identity unconfigured: lint/install requires your values file. Old `backend` and `azure.storageAccountName` settings are rejected rather than silently ignored.

Verify `kubectl config current-context` before changing a cluster. For first-time setup, prepare a nonempty high-entropy ingestion token in a protected file outside the repository, **without whitespace or a trailing newline**:

```bash
kubectl create namespace octocraft
kubectl --namespace octocraft create secret generic octocraft-secrets \
  --from-file=ingest-token=/secure/path/ingest-token

helm lint --strict ./charts/octocraft -f /secure/path/aks-values.yaml
helm template octocraft ./charts/octocraft \
  --namespace octocraft -f /secure/path/aks-values.yaml

# released chart (version = image tag); use ./charts/octocraft for local changes
helm upgrade --install octocraft \
  oci://ghcr.io/strawhat-enterprise/octocraft-charts/octocraft \
  --version 0.2.0 --namespace octocraft -f /secure/path/aks-values.yaml \
  --wait --timeout 5m

kubectl --namespace octocraft port-forward \
  service/octocraft-octocraft 3100:80
```

Open http://localhost:3100. Use the same ingestion token in GitHub IssueOps when a protected, reachable endpoint is available. Missing Secret keys prevent pod startup; missing, empty or whitespace-containing token files cause explicit Go initialization errors, not a development-token fallback.

The Go server reads `INGEST_TOKEN_FILE` and optional `GITHUB_TOKEN_FILE` at startup. It rejects a token configured both as a file and a nonempty value environment variable. Input tokens remain in memory; Kubernetes Secret encryption at rest and namespace access controls remain cluster/operator responsibilities. Mounted files are not a substitute for protecting Kubernetes Secrets.

For optional issue comments, add a GitHub token key to the existing Secret and set `secrets.githubTokenKey`. No Azure Functions `@Microsoft.KeyVault(...)` reference syntax is used inside containers.

## Authentication boundary

Workload Identity authenticates the **pod to Key Vault**, not the browser user. The standalone Go container still does not include Functions Easy Auth. ClusterIP prevents public exposure but other cluster workloads can reach it.

Use an isolated test cluster and localhost port-forwarding until Entra browser authentication/authorization, TLS and network restrictions are implemented. An eventual gateway must distinguish the bearer-protected IssueOps ingest endpoint from browser-protected UI, management and callback endpoints.

## Values

| Value | Behavior |
|---|---|
| `image.repository`, `image.tag`, `image.pullPolicy` | Container image; defaults to `ghcr.io/strawhat-enterprise/octocraft`, and an empty tag uses the chart `appVersion` |
| `imagePullSecrets` | Existing registry Secret references |
| `replicaCount` | Exactly `1` |
| `app.baseURL` | Browser-facing origin for real GitHub callbacks |
| `app.allowSimulate` | Default `false`; synthetic demonstrations only |
| `keyVault.url` | Required real Azure Key Vault URL |
| `workloadIdentity.clientId`, `workloadIdentity.tenantId` | Required identity configuration; no password |
| `secrets.existingSecret`, `secrets.ingestTokenKey` | Existing input-token Secret and key |
| `secrets.githubTokenKey` | Optional GitHub issue-comment token key |
| `persistence.existingClaim` | Optional existing PVC instead of creating one |
| `persistence.size`, `persistence.storageClass` | Default 1Gi/`managed-csi`; custom classes and existing claims must also use a backend without a Storage Account |
| `resources` | Requests 100m CPU/128Mi RAM; limits 500m CPU/256Mi RAM; tune from measurements |

## Upgrades, migration and recovery

- Deploy an updated image: old Go binaries do not support the new token-file and vault-only settings. Secret rotations require `kubectl --namespace octocraft rollout restart deployment/octocraft-octocraft`.
- Recreate causes downtime. Retain the same release/namespace to preserve storage and federation.
- Existing email requests must be approved with Vault delivery instead. API callers must send `secret_storage: vault`. Historical completed email requests retain metadata, but downloads are disabled.
- Existing mock-vault files and ZIPs are **not** deleted or migrated automatically. Back up, migrate existing credentials to Key Vault, and remove old credential-bearing files under an explicit retention procedure.
- If migrating from the former Azure Table/Blob chart profile, export and import request history separately; selecting a PVC does not copy cloud data. The chart's field validation rejects old profile values.
- Key Vault failures are surfaced without a filesystem fallback. GitHub manifest conversion codes are single-use; a Vault failure after conversion requires operator recovery, not blindly retrying the code. `/api/health` does not verify write access. Verify actual access before creating a real app.
- `helm uninstall octocraft --namespace octocraft` retains chart-created PVCs. Key Vault, existing Secrets and Azure identities are not deleted by Helm.
- Reinstall with retained data using `persistence.existingClaim=octocraft-octocraft`.
- Back up data before storage-class/size changes. Delete retained claims only after an explicit decision: a `Delete` reclaim policy can remove the disk.

## Validation and costs

```bash
bash test/helm-chart.sh
go test ./internal/factory
```

Tests exercise PVC rendering, Workload Identity, secret-file mounts, absence of storage-account settings, mandatory Vault configuration and vault-only policy. Local tests use synthetic credentials, not live Azure.

The workflow validates the chart under `build` before approval. It still deploys Functions, not Helm; no AKS rollout or cloud provisioning has been added. AKS nodes, managed disks, registry, Key Vault and logs incur separate charges. The Functions cost model is not an AKS estimate.
