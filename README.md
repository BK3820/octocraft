# GitHub App Factory

Approval portal for creating GitHub Apps. Requests arrive as **IssueOps** issues today (ServiceNow later). An approver clicks **Create** in the web app, and the app's credentials are delivered to **Vault / Azure Key Vault** or as an **emailed ZIP**.

Hosted on **Azure Functions (Flex Consumption)**: https://strawhat-ghapp-factory.azurewebsites.net (Entra ID sign-in)

```
Issue (form) ─► IssueOps Action ─POST /api/ingest─► Function App (Table Storage: pending list)
                                                       │ approver clicks "Create on GitHub"
                                                       ▼
                                       GitHub App Manifest flow (github.com)
                                                       │ redirect /callback?code=…
                                                       ▼
                    exchange code → app id, private key, client secret, webhook secret
                                                       │
                              ┌────────────────────────┴───────────────────────┐
                              ▼                                                ▼
          Vault: HashiCorp Vault (if configured) or Azure Key Vault     ZIP + .eml → Blob "outbox"
```

## Request a GitHub App (IssueOps)

1. **Issues → New issue → "GitHub App request"**, fill the form, and submit.
2. [`issueops-github-app.yml`](.github/workflows/issueops-github-app.yml) parses the form ([`scripts/issue-to-request.js`](scripts/issue-to-request.js)), builds the manifest, and pushes it to the web app. It then comments the request ID and manifest on the issue and adds `awaiting-approval`.
3. An approver opens the web app, picks a delivery method (Vault or Email ZIP), and clicks **Create on GitHub →**. GitHub shows its confirmation page, and the credentials are delivered once you confirm.

To retry a failed request, fix the issue and re-apply the `github-app-request` label.

## CI/CD

[`deploy.yml`](.github/workflows/deploy.yml) runs on every push to `main`: `npm ci` → `npm test` → package → `azure/login` (OIDC, no secrets) → `Azure/functions-action` → `/api/health` smoke test. Pull requests run build and test only.

Repo settings (managed by `scripts/provision.sh`):

| Name | Kind | Purpose |
|---|---|---|
| `AZURE_CLIENT_ID`, `AZURE_TENANT_ID`, `AZURE_SUBSCRIPTION_ID` | variable | OIDC federated identity `github-app-factory-deployer` (subject `repo:<owner>@<id>/<repo>@<id>:ref:refs/heads/main`, *Website Contributor* on the app only) |
| `AZURE_FUNCTIONAPP_NAME`, `APP_FACTORY_URL` | variable | Deploy target / ingest URL |
| `APP_FACTORY_INGEST_TOKEN` | secret | Bearer token for `/api/ingest` (source of truth: Key Vault secret `ingest-token`) |

## Azure infrastructure

[`infra/main.bicep`](infra/main.bicep) deploys Flex Consumption plan (Node 22), Function App with a system-assigned identity, Storage (shared-key **disabled**; Table `apprequests`, Blob `outbox` and `deployments`), Key Vault (RBAC), App Insights + Log Analytics, and Easy Auth (Entra ID) on everything except `/api/ingest` and `/api/health`.

```bash
az login && gh auth login
./scripts/provision.sh        # idempotent: Entra apps, OIDC, Bicep, GitHub vars/secrets/labels
```

Override `REPO`, `RG`, `LOCATION`, or `APP_NAME` with env vars.

## Run locally

```bash
npm install
npm start            # http://localhost:3000 — local JSON store, mock vault, local outbox
npm run seed         # queue 3 sample requests (same parser as the Action)
npm test
```

Use `VAULT_ADDR=… VAULT_TOKEN=… npm start` to write to a real HashiCorp Vault (KV v2).

## Configuration

| Env var | Default | Purpose |
|---|---|---|
| `INGEST_TOKEN` | `dev-ingest-token` | Bearer token for `POST /api/ingest` |
| `BASE_URL` | request origin | Used for the manifest `redirect_url` |
| `STORAGE_ACCOUNT_NAME`, `REQUESTS_TABLE`, `OUTBOX_CONTAINER` | local files | Azure Table/Blob via managed identity |
| `VAULT_ADDR`, `VAULT_TOKEN`, `VAULT_KV_MOUNT`, `VAULT_PATH_PREFIX` | – | HashiCorp Vault (takes precedence) |
| `KEY_VAULT_URL` | – | Azure Key Vault. Secrets are `ghapp-<slug>-private-key`, `-client-secret`, and `-webhook-secret` |
| `GITHUB_WEB_URL` / `GITHUB_API_URL` | github.com | GHES / GHE.com |
| `GITHUB_TOKEN` | – | Optional. Comments the result back on the source issue |
| `ALLOW_SIMULATE` | `true` | Shows "Simulate create" (fake credentials, demo only) |

### Ingest API (ServiceNow)

`POST /api/ingest` with `Authorization: Bearer <token>`:

```json
{
  "owner_type": "org", "owner": "StrawHat-Enterprise",
  "secret_storage": "vault", "notify_email": "team@example.com",
  "requester": "jdoe",
  "source": { "type": "servicenow", "ticket": "RITM0012345" },
  "manifest": { "name": "my-app", "url": "https://…", "default_permissions": { "contents": "read" }, "default_events": [] }
}
```

## Known gaps (POC)
- Email isn't sent. The ZIP and `.eml` go to the Blob `outbox`. Wire up Azure Communication Services Email or SMTP next, and encrypt the ZIP.
- Any user in the Entra tenant can sign in. Set "Assignment required" on the `github-app-factory-ui` enterprise app to restrict approvers.
- Commenting the result back on the issue needs a `GITHUB_TOKEN` app setting, ideally a GitHub App installation token.
