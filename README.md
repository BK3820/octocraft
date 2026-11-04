# GitHub App Factory

GitHub App Factory is an approval portal for creating GitHub Apps from **IssueOps** requests. A requester opens an issue, the workflow creates a pending manifest, and an approver uses the web UI to create the GitHub App and select credential delivery.

> **Current state:** Azure runtime resources are intentionally removed. The repository, infrastructure-as-code, Entra app registrations, and GitHub OIDC configuration are retained so the app can be recreated when needed.

## What the app does

```text
GitHub issue form
  → IssueOps workflow builds a GitHub App manifest
  → POST /api/ingest creates a pending request
  → approver opens the web UI and clicks Create on GitHub
  → GitHub App Manifest flow returns the app credentials
  → credentials go to HashiCorp Vault / Azure Key Vault, or a ZIP in the outbox
```

Email delivery is **not automatic** yet. The email option writes a ZIP and `.eml` draft to the outbox. Add Azure Communication Services Email or SMTP, and encrypt the ZIP, before using that path in production.

## Recreate the Azure deployment

Follow these steps in order when you are ready to deploy again.

### 1. Get the code and install dependencies

```bash
git clone https://github.com/StrawHat-Enterprise/github-app-factory.git
cd github-app-factory
npm ci
npm test
```

### 2. Sign in and select the correct subscription

You need Azure **Owner** or equivalent permission to create role assignments, plus GitHub admin permission on `StrawHat-Enterprise/github-app-factory`.

```bash
az login
az account set --subscription "Azure subscription 1"
gh auth login
```

Verify the active account before continuing:

```bash
az account show --query '{subscription:name,tenant:tenantId,user:user.name}' --output json
gh auth status
```

### 3. Choose names and region

The defaults are:

| Setting | Default |
|---|---|
| Resource group | `rg-github-app-factory` |
| Region | `eastus2` |
| Function App | `strawhat-ghapp-factory` |
| Repository | `StrawHat-Enterprise/github-app-factory` |

Override them only if needed:

```bash
export RG=rg-github-app-factory
export LOCATION=eastus2
export APP_NAME=strawhat-ghapp-factory
export REPO=StrawHat-Enterprise/github-app-factory
```

### 4. Provision Azure and GitHub configuration

```bash
./scripts/provision.sh
```

The script is idempotent. It:

1. Creates or reuses the Entra application for UI sign-in.
2. Creates or reuses the GitHub Actions OIDC application and federated credential.
3. Creates the Function App on Azure Functions Flex Consumption.
4. Creates Storage, Key Vault, Application Insights, and Log Analytics.
5. Grants the Function App managed identity only the roles it needs.
6. Sets GitHub repository variables, the ingestion-token secret, and IssueOps labels.
7. Sets `APP_FACTORY_ENABLED=true`, enabling deployment and IssueOps ingestion jobs.

### 5. Deploy the application code

Push a commit to `main`, or manually run **Build and deploy** from the repository’s Actions tab.

```bash
git push origin main
```

The [`deploy.yml`](.github/workflows/deploy.yml) workflow runs:

```text
npm ci → npm test → package Function App → Azure OIDC login → deploy → /api/health smoke test
```

No Azure password or publishing profile is stored in GitHub.

### 6. Verify the app

```bash
curl -sS "https://${APP_NAME}.azurewebsites.net/api/health"
```

Expected response:

```json
{"ok":true,"store":"azure-table"}
```

Open `https://${APP_NAME}.azurewebsites.net` in a browser and sign in with Entra ID. The UI is protected by App Service Authentication; only `/api/health` and the token-protected `/api/ingest` endpoint are public.

### 7. Create a GitHub App request

1. In the repository, select **Issues → New issue → GitHub App request**.
2. Complete the app name, organization, homepage, permissions, webhook, and secret-delivery fields.
3. Submit the issue.
4. The [`issueops-github-app.yml`](.github/workflows/issueops-github-app.yml) workflow parses the form with [`scripts/issue-to-request.js`](scripts/issue-to-request.js), posts the manifest to the app, comments the request URL, and applies `awaiting-approval`.
5. Sign in to the web UI, inspect the pending request, choose Vault or Email ZIP, and select **Create on GitHub →**.
6. Confirm the GitHub App Manifest screen. GitHub redirects back to the app, which stores the returned credentials.

To retry a failed request, correct the issue and remove then re-add the `github-app-request` label.

## Pause or remove the deployment

To stop remote deployments and IssueOps ingestion and delete the Azure runtime:

```bash
./scripts/deprovision.sh --confirm
```

This deletes **every resource** in `rg-github-app-factory`, including the Function App, Storage data and outbox ZIPs, Key Vault secrets, monitoring, and the Flex Consumption plan. It also purges the soft-deleted Key Vault so the same deployment names can be used again. Deleted data cannot be recovered.

The script deliberately retains these no-cost identity/configuration items for a later rebuild:

- `github-app-factory-ui` Entra app registration
- `github-app-factory-deployer` Entra app registration and GitHub OIDC federated credential
- GitHub Actions variables, ingestion-token secret, labels, workflows, and source code

It also sets the repository variable `APP_FACTORY_ENABLED=false`. The build/test job can still run, but deployment and IssueOps ingestion jobs are skipped until `./scripts/provision.sh` enables them again.

## Run locally

```bash
npm ci
npm start
# In a second terminal:
npm run seed
```

Browse to http://localhost:3000. Local mode uses `data/requests.json`, a mock Vault file, and `data/outbox/`; all are ignored by Git.

To test a real HashiCorp Vault locally:

```bash
vault server -dev -dev-root-token-id=root
VAULT_ADDR=http://127.0.0.1:8200 VAULT_TOKEN=root npm start
```

## Runtime configuration

| Variable | Local default | Azure use |
|---|---|---|
| `INGEST_TOKEN` | `dev-ingest-token` | Key Vault reference for IssueOps/ServiceNow ingestion |
| `BASE_URL` | Request origin | GitHub manifest callback URL |
| `STORAGE_ACCOUNT_NAME` | Unset | Enables managed-identity Azure Table and Blob storage |
| `REQUESTS_TABLE` / `OUTBOX_CONTAINER` | `apprequests` / `outbox` | Storage resource names |
| `KEY_VAULT_URL` | Unset | Stores GitHub credentials in Azure Key Vault |
| `VAULT_ADDR`, `VAULT_TOKEN` | Unset | Uses HashiCorp Vault instead of Key Vault |
| `GITHUB_WEB_URL` / `GITHUB_API_URL` | `github.com` | GitHub Enterprise endpoints |
| `GITHUB_TOKEN` | Unset | Optional comment back to the source issue |
| `ALLOW_SIMULATE` | `true` | Shows simulated GitHub App creation for the POC |

## ServiceNow integration later

ServiceNow can use the same token-protected endpoint as the IssueOps workflow:

```http
POST /api/ingest
Authorization: Bearer <INGEST_TOKEN>
Content-Type: application/json
```

```json
{
  "owner_type": "org",
  "owner": "StrawHat-Enterprise",
  "secret_storage": "vault",
  "requester": "jdoe",
  "source": { "type": "servicenow", "ticket": "RITM0012345" },
  "manifest": {
    "name": "my-app",
    "url": "https://example.com",
    "default_permissions": { "contents": "read" },
    "default_events": []
  }
}
```
