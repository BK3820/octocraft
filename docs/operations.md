# Deployment, state, approval and recovery runbook

## Current state and boundaries

- Azure runtime is absent. Do not point IssueOps at the old hostname expecting a live service.
- Entra UI/deployer registrations from the earlier POC are retained, and have no app runtime resources.
- Terraform validates locally; no Terraform apply has been run for this conversion.
- `APP_FACTORY_ENABLED=false` protects deployment and IssueOps jobs from contacting removed services.
- GitHub’s existing plan rejects private rulesets and required deployment reviewers. Upgrade to the appropriate Enterprise plan before enabling deployment.
- The production environment exists with main-only branches and admin bypass disabled, but no required-reviewer rule yet. Squash-only merging and merged-branch deletion are already configured.
- Infrastructure is managed interactively by Terraform; the GitHub pipeline deploys **Go Functions application code only**, not infrastructure.
- The Function App is configured for the native Go `1.0` worker on Linux Flex Consumption. Go packaging requires Core Tools 4.12 or later, which currently prints a preview warning.
- CI builds the Functions ZIP for deployment and separately builds/runs the standalone Docker image as a health check. The image is not pushed to a registry or deployed by this workflow.
- The [Helm chart](../charts/github-app-factory/README.md) uses a managed-disk PVC for requests and Key Vault through Workload Identity for credentials, with no Storage Account configuration. Input tokens are mounted files, and `VAULT_ONLY=true` blocks ZIP delivery/downloads and mock-vault fallback. CI validates it under `build`, but does not create AKS resources, install Helm, or alter the separate Functions deployment. It is internal-only and single-replica; browser authentication remains a prerequisite.

## Terraform inventory and state

Providers are locked: AzureRM for native Azure resources, AzAPI for management-plane Blob/table/secret creation and the native Go runtime patch, Random for globally unique names and ingestion token.

AzureRM currently excludes `go` from its Flex Consumption runtime enum even though Azure supports `functionAppConfig.runtime = { name = "go", version = "1.0" }`. Terraform therefore creates the Function App with the provider's `custom` bootstrap value and AzAPI immediately patches it to Go in the same apply. The AzureRM lifecycle ignores those two stale provider fields; [runtime.tftest.hcl](../infra/tests/runtime.tftest.hcl) asserts the authoritative AzAPI runtime and prevents an accidental `FUNCTIONS_WORKER_RUNTIME` app setting.

Managed resources: resource group, Storage, two Blob containers, Table, Flex plan and Function App, authentication, vault/ingest secret, Insights/workspace, and scoped role assignments. Entra registrations and GitHub policies are script-managed, not Terraform-managed.

The former Bicep runtime was deleted and its vault purged. Do not attempt to import those nonexistent resources. New random storage/vault names avoid relying on the old Bicep `uniqueString` naming.

### Protect state

1. Local default: keep `infra/terraform.tfstate` and backups on encrypted disk accessible only to the operator.
2. Team setup: use [backend.tf.example](../infra/backend.tf.example) with a separately managed private Blob container and Entra-based RBAC.
3. Enable state container versioning/backup and locking. Do not grant everyone data access.
4. Keep backend outside this deployment’s resource group so `destroy` cannot remove its own state.
5. Back up state before provider upgrades, imports, migrations and destruction.
6. Sensitive output flags suppress normal display but **do not encrypt state**. `terraform output -raw ingest_token` reveals a credential; only pipe it into the intended secret store.
7. Terraform plans can contain secrets too. Do not attach state or plan files to PRs/issues.
8. If moving from local to remote state, configure backend and use `terraform init -migrate-state` under a reviewed backup procedure.

### Direct Terraform use

The wrapper scripts provide identity/GitHub setup. For manual Terraform operations, use the IDs of those registrations:

```bash
export TF_VAR_subscription_id="$(az account show --query id -o tsv)"
export TF_VAR_auth_client_id="<UI client ID>"
export TF_VAR_deployer_principal_id="<deployer service principal object ID>"
terraform -chdir=infra init
terraform -chdir=infra plan
terraform -chdir=infra apply
```

Review resources to create/replace/delete before entering `yes`. Terraform apply is a separate operator approval from GitHub’s code-deployment review. To have GitHub approve infrastructure applies too, a separate privileged IaC workflow and identity would be needed; this repo does not pretend its Website Contributor code deployer can manage all infrastructure.

## Production environment protection

Run `node scripts/configure-github.js` only with repository admin permissions. It leaves `APP_FACTORY_ENABLED=false`, configures reviewer BK3820, `can_admins_bypass:false`, permits only branch `main`, installs the versioned ruleset and sets squash-only merging.

Verify:

```bash
gh api repos/StrawHat-Enterprise/github-app-factory/environments/production
gh api repos/StrawHat-Enterprise/github-app-factory/rulesets
gh variable get APP_FACTORY_ENABLED -R StrawHat-Enterprise/github-app-factory
```

Expected environment policy: a `required_reviewers` rule for BK3820, admin bypass disabled, main-only deployment branch policy. A successful base environment creation without reviewers is **not** enough.

On the current Free plan, the reviewer API returns 422, so the workflow does not rely on the environment rule alone. Its `approval` job opens a deployment issue and blocks until BK3820 comments `approve`. A `deny` comment, a closed issue or the 6-hour timeout fails the job, and `deploy` never runs. Comments from anyone other than BK3820 are ignored. Keep policy-changing workflows/CODEOWNERS behind review; no workflow can defend against an administrator intentionally replacing both policy and code outside governance.

### OIDC subject

The existing repository uses immutable owner/repository ID claim syntax:

```text
repo:StrawHat-Enterprise@<owner-id>/github-app-factory@<repo-id>:environment:production
```

The provisioning script resolves the numeric IDs and registers this subject with Entra. Adding `environment:production` to the job changes the subject from the old branch-only form; leaving old branch federation in place would be unnecessary trust. The script removes other credentials on this dedicated deployer registration before adding the production one.

Do not reuse that dedicated registration for unrelated repositories; otherwise the script’s credential cleanup could remove their federation. If GitHub OIDC subject customization changes, compare the actual `azure/login` subject with Entra and update deliberately.

## Provisioning sequence

1. Finish plan upgrade, resolve second-code-owner issue, merge the implementation PR.
2. `az login`, select subscription, `gh auth status`.
3. Configure/verify GitHub protections.
4. Choose Terraform backend and save all input values.
5. Run `scripts/provision.sh`; it verifies governance before creating resources.
6. Review interactive Terraform apply. Azure registration permissions and regional Flex quota must be available.
7. Allow time for managed identity RBAC and Key Vault app-setting references to propagate.
8. Verify GitHub target variables and ingest secret have been written.
9. Manually set `APP_FACTORY_ENABLED=true`.
10. Dispatch `deploy.yml` on `main` (merges do not deploy). Approve only the correct successful commit.
11. Smoke-test `/api/health`, browser sign-in, authenticated list, and IssueOps ingestion.

If setup fails, it leaves remote jobs disabled. Read the exact failed API/CLI output; do not bypass a permissions failure by enabling anonymous auth, account keys or an unprotected environment.

## What CI proves and what it does not

| Check | Evidence | Not proof of |
|---|---|---|
| `build` | Go and IssueOps parser tests, Go vet, Linux Functions ZIP, Docker build and container health smoke test | Actual GitHub App creation |
| `terraform` | Formatting, provider installation, schema validation and mock-provider policy plan | Azure quota, permissions, applied state, managed identity propagation |
| `ui` | Real Chromium, live local API, search/filter/sort, draft retention, simulate/outbox, mobile width, error display | Entra login or real one-time GitHub callback |
| `release` (main only) | Tag, multi-arch GHCR image, OCI chart and GitHub Release share one version | That the release was deployed anywhere |
| `approval` | BK3820 explicitly commented `approve` on the run's deployment issue | Reviewer’s diligence or Azure health |
| Production wait | Native GitHub review before job starts | Approval of individual app-creation requests |
| `/api/health` | Function handler responds | Table/Vault/Blob readiness; this endpoint does not probe them |

## Troubleshooting

| Symptom | Likely cause / safe action |
|---|---|
| Environment reviewers API 422 / rulesets 403 | Current private-repo plan lacks feature. Upgrade; leave disabled. Do not just remove `environment`. |
| PR cannot merge with BK3820 as author | GitHub forbids own PR approval; add another qualified code owner or explicitly change the policy. |
| Azure login `AADSTS700213` | OIDC subject/issuer/audience mismatch. Check environment subject including IDs, not only repo name. |
| UI 401 from curl but browser redirects | Easy Auth negotiates HTML sign-in vs API response. Use a browser; do not make UI anonymous. |
| Ingest 401 | GitHub secret and app Key Vault reference differ/not propagated; verify configuration without printing tokens. |
| Ingest 409 | Duplicate app name. Workflow intentionally does not re-comment. Edit request/name or use history; avoid duplicate create. |
| Table/Blob 403 after apply | RBAC propagation or wrong principal/scope; inspect roles, wait, retry. Do not enable account keys. |
| Web UI empty after recreation | Old Storage was deleted; prior GitHub issues remain but are not automatically replayed. Reapply intake label where appropriate. |
| Job waits for approval | Expected. BK3820 must comment `approve` on the deployment issue (and, after the upgrade, review `production`). No Azure login happens before approval. |
| Job skips after push | Expected while disabled or on non-main refs. Manual dispatch on feature branches does not deploy. |
| Callback invalid state | Request reset/restarted or mismatched/missing state. Do not disable validation. |
| App exists but portal says waiting | Conversion may have succeeded before delivery failure. Reconcile GitHub app settings and rotate/recover credentials manually. |
| Email recipient gets nothing | Expected: only ZIP + `.eml` outbox implemented. Configure a real provider as a separate change. |
| Terraform says resources already exist | Wrong/lost state or unmanaged resource. Restore state/import under review; do not blindly delete or force replacement. |

## Rollback

For code: revert through a reviewed PR, run checks, approve deployment. Manual workflow dispatch is not an artifact-by-ID rollback; it builds the selected main revision. Record the deployed commit and any successful release tag.

For infrastructure: inspect a new plan; reverting HCL can still replace resources and destroy data. Restore from backed-up state/data where possible. Terraform is not a data backup. Recreating a storage account does not recreate old request history.

## Pause and destroy

1. Set `APP_FACTORY_ENABLED=false`.
2. Cancel active/waiting deployments; changing the flag cannot stop an already-started job.
3. Export any needed request audit/history and recover required GitHub credentials. Never export them into Git.
4. Verify the exact subscription, state backend, workspace and resource plan.
5. `./scripts/deprovision.sh --confirm`, then review Terraform’s interactive destroy plan.
6. POC Key Vault purge is enabled and purge protection disabled. This permanently destroys retained vault secrets; do not use that policy for regulated production without review.
7. Verify the Azure resource group is absent and state has no managed runtime resources.
8. Keep Entra registrations, repo and backend if recreating later. Optional backend resources need their own teardown/cost review.

An empty state is not evidence that an unrelated or formerly unmanaged resource group is empty. The script reports no Terraform-managed resources and does not delete unknown Azure resources.

## Production work still required

- Approver allowlist or Entra enterprise-app assignment/group roles. Current tenant sign-in is broader than authorized business approvers.
- Atomic, conditional row updates, immutable audit events, distributed idempotency and locking.
- Full validation of manifest URLs/permissions/payload shape and threat review before public production intake.
- Durable encrypted recovery after one-time GitHub conversion; delivery retry/reconciliation without creating another app.
- Outbox encryption, recipient authorization, retention/deletion, real email provider and separate password/key distribution if ZIP delivery remains.
- GitHub App installation-token result commenting and label reconciliation. Optional current token commentary does not close the issue automatically.
- Storage/Vault dependency readiness probes, alert rules, budgets and log retention policy.
- Pagination/archival/delta refresh rather than scanning all requests.
- Terraform remote state backup/locking, least-privilege operator role and tracked infrastructure change approvals.
- Final real Azure apply plus Entra/browser/GitHub manifest-flow integration test after redeployment. Local tests are not a substitute.
