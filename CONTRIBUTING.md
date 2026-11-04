# Contributing and Git strategy

## Branch model: trunk-based, PR-first

- `main` is the releasable branch; no long-lived `develop` branch.
- Make short-lived branches: `feat/<topic>`, `fix/<topic>`, `docs/<topic>`, `infra/<topic>`.
- Open a PR early for substantial changes; use Draft while incomplete.
- One coherent change per PR. Keep unrelated cleanup out.
- Squash merge after reviews/checks; delete the branch after merge.
- Release tags use `vMAJOR.MINOR.PATCH` only for intentional releases. Tags do not themselves deploy.
- Deployment follows `main` or a manual workflow dispatch on `main`, and still needs production approval.

```bash
git switch main
git pull --ff-only
git switch -c feat/my-change
# edit and validate
git add <specific-paths>
git commit -m "feat: describe the behavior change"
git push -u origin feat/my-change
gh pr create --base main
```

Use meaningful `feat:`, `fix:`, `docs:`, `infra:`, or `test:` commit subjects. Explain behavior changes, migration and rollback risks in the PR body. Do not force-push `main`. If refreshing your feature branch, use `--force-with-lease`, never an unconditional force push.

## Main ruleset

The policy is version-controlled in [.github/rulesets/main.json](.github/rulesets/main.json), and installed through:

```bash
node scripts/configure-github.js
```

The current organization plan has refused enforcement for this private repository. A policy file is **not** an enforced ruleset. Until the upgrade and successful API verification, follow these policies manually and keep deployment disabled.

| Policy | Purpose |
|---|---|
| PR required, at least one approval | No direct-to-main implementation changes |
| Code owner review required | BK3820 owns the repo and deployment-sensitive changes |
| Dismiss stale approvals | New changes must be reviewed again |
| Last push approval required | The person pushing the final changes cannot be the only approver |
| Resolve review threads | Do not merge unresolved discussions |
| `build`, `terraform`, `ui` checks from GitHub Actions | Unit/package, infrastructure validation, browser behavior |
| Branch must be up to date | Test changes against latest main before merge |
| Linear history | Squash commits for reviewable rollback/history |
| Block deletion and force pushes | Protect the release branch |
| No bypass actors | Administrators are not a normal path around review |

**Solo-maintainer nuance:** GitHub does not allow PR authors to approve their own PR. With only BK3820 as code owner and mandatory last-push approval, a BK3820-authored PR needs another qualified code owner. Before enforcing the ruleset, add an additional maintainer/team to [CODEOWNERS](.github/CODEOWNERS), or deliberately revise the approval policy. Do not weaken the policy silently to make a merge work.

## Required local validation

```bash
npm ci
npm test
npx playwright install chromium
npm run test:ui
terraform -chdir=infra init -backend=false
terraform -chdir=infra fmt -check
terraform -chdir=infra validate
terraform -chdir=infra test
bash -n scripts/provision.sh scripts/deprovision.sh
git diff --check
```

- Backend behavior: add/adjust Node tests for validation, statuses, credentials and API responses.
- UI changes: keep keyboard focus, labels, mobile layout, loading/error states and draft preservation. Run `npm run screenshots` and commit refreshed images with the UI.
- Infrastructure: include `terraform plan` review when an environment exists. CI does not run apply or authenticated cloud plans.
- Dependency changes: commit the npm lockfile. Provider changes: commit the Terraform lockfile with both `linux_amd64` and `darwin_arm64` checksums.
- Do not check in `.terraform/`, state, tfvars containing account IDs/secrets, plan files, local test data, ZIPs, PEMs or credentials.
- Never apply/destroy Azure resources just to test a documentation/UI change.

## PR review checklist

1. Does the change implement the requested behavior and preserve existing flows?
2. Do new permissions stay scoped to app-owned resources?
3. Are error paths visible and described, especially after GitHub’s one-time code exchange?
4. Could Terraform replace the storage account, vault, or Function App? Explain data impact.
5. Do documentation diagrams, screenshots and cost assumptions still match the code?
6. Is there a credible rollback? A code revert cannot restore deleted storage/secret data.
7. Are all checks passing on the latest commit and all review threads resolved?
8. Does the reviewer understand whether the feature is real or simulated?

## Merge approval versus deployment approval

These are separate decisions:

1. PR review permits code to enter `main`.
2. CI runs build, unit tests, Terraform validation and browser tests.
3. If `APP_FACTORY_ENABLED=true`, approval-policy checks the real `production` reviewer configuration.
4. GitHub pauses the deployment job at `production` for **BK3820**.
5. Only approval releases that job to Azure OIDC login and code deployment.
6. IssueOps requests are subsequently approved by a person in the web UI; that is a third independent decision.

Deployment self-review is permitted for the POC because the requested sole deployment reviewer is BK3820, who may also initiate the run. Admin bypass is disabled. For a separation-of-duties production policy, add another reviewer and enable `prevent_self_review`; with only BK3820, their own deployment could otherwise wait forever.

## Releases, rollback and emergencies

- Release by merging a reviewed PR, approving its production job, then recording the commit/tag and smoke-test result.
- Do not enable auto-merge; owner approval is deliberate.
- Standard rollback: revert via PR, run checks, approve redeployment.
- Manual dispatch builds the chosen `main` revision; selecting a feature ref does not satisfy the deployment condition.
- If CI is waiting on an obsolete commit, reject/cancel it. Never approve old and new deployments concurrently.
- In an incident, first pause `APP_FACTORY_ENABLED` and cancel queued deployment jobs. The flag does not cancel already-running jobs.
- Any emergency bypass policy change must be explicit, recorded, time-bounded and restored; do not add permanent admin bypass.
