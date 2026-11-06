# GitHub Platform Control Plane Design

Status: Proposed for review. The hybrid approach was selected for exploration; this document still needs approval before an implementation plan is written.

## Intent

Evolve OctoCraft into an open-source, self-hosted control plane for platform teams that own GitHub organizations. Give developers and service teams a safe way to request administrative work they cannot perform with their assigned GitHub roles, while keeping elevated authority with organization or enterprise owners.

The system coordinates requests, approvals, policy checks, execution, and audit. It does not bypass GitHub authorization. A GitHub owner must explicitly install and configure the integration, and each operation must be supported by GitHub's APIs and granted permissions. Unsupported operations must be identified as requiring an administrator, not represented as successfully automated.

## Why

Teams currently have to route work such as mapping a team slug to a repository, creating repositories from approved templates, granting or removing access, and managing enterprise Actions allowlists to a small number of administrators. Custom roles do not cover every organization- or enterprise-level operation. This creates queues, inconsistent access, and permission drift.

The broader initiative also aims to centralize ServiceNow work intake, policy CRUD, repository backup, GitHub Actions usage and cost-center analytics, and integrations with Vault, Azure, and AWS.

## Current State

OctoCraft is a Go service with a static HTML/CSS/JavaScript UI. Its current workflow accepts issue-based requests, queues them for human review, creates GitHub Apps through GitHub's manifest flow, and stores or packages the resulting credentials. The current issue workflow stands in for ServiceNow; it does not poll ServiceNow or execute organization-wide repository and policy operations. The architecture has Azure and Vault paths for the existing GitHub App credential handoff, but the Azure deployment is currently disabled/deleted.

The existing approval queue, request lifecycle, Go API, and credential-handling boundaries are useful foundations. The new control plane should extend those boundaries rather than turn the first release into a rewrite.

## Goals

- Let non-admin users submit structured requests for supported GitHub administration tasks.
- Map GitHub team slugs to repository access levels and show who has access to each repository, including inherited and direct grants where APIs expose them.
- Provision repositories from approved templates with standard teams and settings.
- Manage policy records through CRUD operations, with validation, version history, and authorization.
- Route sensitive or privileged changes through explicit approval before execution.
- Show permission and policy drift; provide a safe path to remediate it.
- Support ServiceNow as an intake system that creates the same validated jobs as the UI.
- Keep an auditable record linking requester, ticket, approval, operation, GitHub result, and before/after state.
- Add Vault, Azure, and AWS integrations without coupling core request workflows to one provider.
- Provide repository backup and GitHub Actions usage/cost-center analytics in later phases.
- Keep the project usable as self-hosted open-source software, with no central SaaS dependency.

## Non-Goals

- Replacing GitHub's own repository, organization, or enterprise settings interfaces.
- Bypassing GitHub's permission model or using an administrator's identity invisibly.
- Automatically executing arbitrary commands or code taken from a ServiceNow ticket.
- Promising automation for GitHub settings that are not available through supported APIs or the installed integration's permissions.
- Building a general-purpose cloud management platform for unrelated Azure or AWS resources.
- Automatically correcting all detected drift in the first release.

## Selected Approach: Hybrid Control Plane

The selected direction combines an approval-backed request and execution hub with policy-as-configuration and progressive drift reconciliation.

1. A requester or ServiceNow integration creates a structured job.
2. The system validates the request against configured policy and the integration's capability registry.
3. The UI shows the requested changes, affected repositories/teams, required authority, and a dry-run summary when available.
4. Required approvers approve or reject the operation.
5. A worker executes supported changes through the owner-configured GitHub integration and records the result.
6. Scheduled inventory compares observed state with policy. The first release reports drift; later releases can prepare approved remediation jobs or reconcile explicitly opted-in policies.

This approach uses OctoCraft's existing approval workflow, adds immediate visibility into access drift, and avoids making silent, automatic mutation the default. A full GitOps reconciler is deferred until the request model, policies, and GitHub API coverage are established.

## Alternatives Considered

### Approval and execution hub only

Extend the existing queue to handle repository, team-access, and policy requests, then execute approved operations. This is the smallest change and directly reduces administrator bottlenecks, but it does not provide a durable desired-state model or a strong long-term drift loop.

### Policy reconciler first

Define desired organization state in configuration and have a scheduled controller detect and correct differences. This offers strong consistency and an auditable source of truth, but requires users to adopt a configuration model before they can get value. It also cannot automate API-unavailable settings and risks surprising users if remediation is automatic.

### Hybrid control plane (selected)

Start with structured requests, approvals, execution, audit, and read-only inventory. Gradually make policy configuration the source for drift detection and opt-in remediation. This balances immediate delegation with a safe path toward consistent desired state.

## Users and Roles

- **Requester:** submits an administrative request and views its status; does not need organization admin rights.
- **Approver:** reviews the plan and approves or rejects operations based on configured policy.
- **GitHub owner/operator:** installs the GitHub App, grants required permissions, configures organization/enterprise connections, and manages approvers and policies.
- **Auditor:** reads policy, inventory, job history, and audit records without executing operations.
- **Integration worker:** polls configured ticket sources, scans GitHub state, and executes approved jobs under the configured service identity.

Role assignments and approval thresholds must be configurable. The system must not infer that a ticket author is authorized to approve or execute the ticket's requested changes.

## Functional Scope and Phases

### Phase 1: Delegated GitHub administration MVP

- Support one GitHub.com organization per initial installation; keep internal models ready for additional organizations later.
- Register an owner-installed GitHub App using the minimum permissions required for the selected capabilities.
- Provide a capability registry that classifies every operation as executable, approval-required, administrator-action-required, or unsupported for the current GitHub plan/API/auth mode.
- Support request and approval flows for repository creation from a configured template and team-slug-to-repository access changes.
- Show a read-only inventory of repositories, teams, and access grants that GitHub exposes through granted permissions.
- Provide CRUD management for policy records covering approved templates, allowed access levels, default teams, and approval rules.
- Show dry-run plans and require approval before mutations. Record idempotency keys and operation outcomes to prevent duplicate execution.
- Detect access drift against configured policy and present proposed fixes as requests; do not auto-correct in this phase.
- Extend audit records to include requester, approver, policy version, affected resources, operation ID, timestamps, API response classification, and before/after values where available.

### Phase 2: Enterprise policy and ServiceNow

- Add a ServiceNow connector with configurable scheduled polling, pagination/cursors, backoff, deduplication, and ticket status updates.
- Convert tickets into the same validated job model used by the UI. Ticket text is input data, never an executable instruction.
- Support external GitHub Actions allowlist management only where GitHub exposes the required enterprise/org API and the installation has the required owner-granted authority.
- If an allowlist change cannot be performed through the current integration, create a clearly labeled administrator task with the exact setting and intended change; never report it as completed.
- Add policy versions and policy-change approvals for sensitive configuration changes.

### Phase 3: Opt-in reconciliation and repository backup

- Allow selected policies to run in report-only, propose-remediation, or explicitly enabled reconcile mode.
- Require a policy owner, documented scope, and audit event for reconcile mode. Provide pause/disable controls and a clear last-success/last-error status.
- Add repository backup jobs to configured object storage, with encryption, retention, integrity checks, restore verification, and documented limits on which GitHub metadata is included.
- Keep backup execution separate from repository administration permissions where possible.

### Phase 4: Integrations and analytics

- Add Vault integration for secrets and provider credentials, following the existing credential-handoff security boundary.
- Add Azure and AWS deployment/storage adapters behind provider interfaces; core workflows must remain usable without either cloud.
- Add GitHub Actions usage analytics by repository and cost center, with source, refresh time, attribution method, and missing-data caveats visible.
- Add organization-level views for policy compliance, access changes, repository ownership, backup health, and Actions usage.

## Security and Permission Model

- A GitHub organization or enterprise owner explicitly installs and configures the GitHub App. The service never claims to grant itself authority.
- Prefer GitHub App installation tokens and narrowly scoped permissions. Do not use a personal access token as an undocumented fallback.
- Keep organization- and enterprise-level operations distinct from repository-level operations in the capability registry and UI.
- Validate every job against the current policy and integration capability at both submission and execution time.
- Require a second approval or an owner-level approval for policy-defined high-risk operations, including broad access grants, visibility changes, enterprise allowlist changes, and destructive operations.
- Store credentials only in the configured secret backend. Redact secrets and tokens from application, job, and audit logs.
- Treat ServiceNow, GitHub webhooks, and user-submitted fields as untrusted input. Use allowlisted operation types and typed parameters, not arbitrary scripts.
- Provide an append-only audit trail for policy changes, approvals, execution, retries, and manual administrator actions.
- Do not silently remove access or modify settings during drift scans. Remediation is an explicit, attributable operation.

## Technical Direction

- Preserve the current Go HTTP service and static UI for the first implementation slice.
- Extend the existing request lifecycle with typed job kinds, approvals, execution status, retry handling, and audit records rather than embedding provider-specific logic in HTTP handlers.
- Introduce narrow interfaces for GitHub, ticketing, secret storage, scheduled scans, backup storage, and usage data. Ship integrations incrementally; do not require Azure or AWS for local/self-hosted operation.
- Use one job model for UI requests, ServiceNow tickets, scheduled scans, and remediation proposals.
- Make scheduled work idempotent and observable, with bounded retries, a cursor/checkpoint, and an operator-visible failure state.
- Keep policy serialization/versioning portable. The initial UX is policy CRUD in the application; YAML import/export can be added after the policy schema stabilizes.
- No Kubernetes Custom Resource Definition API is included in the initial scope. If “CRD” meant Kubernetes CRDs rather than CRUD policy management, that changes the configuration interface and should be decided before planning that slice.

## Success Measures

- Requesters can initiate approved repo and team-access tasks without being granted GitHub admin rights.
- Every supported mutation is traceable to a request/ticket, policy evaluation, approver, and GitHub result.
- Unsupported or owner-only operations are identified before approval/execution, not discovered as opaque runtime failures.
- The access inventory surfaces direct, inherited, and team-derived grants where available, and reports when the integration lacks visibility.
- Duplicate ServiceNow polls or retries do not create duplicate jobs or repeat non-idempotent mutations.
- Drift is measurable, and remediation is reviewable and reversible where the GitHub API permits.
- No secrets are emitted in logs or exposed to requesters who lack access to the configured secret backend.
- Analytics show source data and attribution limitations so estimates are not presented as authoritative billing.

## Open Decisions Before Implementation Planning

- Confirm whether the initial deployment targets GitHub.com only, or must support GitHub Enterprise Server from the first release.
- Confirm whether “CRD” in the request meant CRUD policy management or Kubernetes Custom Resource Definitions. This design assumes CRUD policy management and defers Kubernetes CRDs.
- Identify which ServiceNow ticket type, fields, status transitions, and polling cadence the first connector must support.
- Define the intended repository backup scope: Git data only, or also issues, pull requests, releases, metadata, and configuration.
- Confirm whether multi-organization support is required in the first release or can follow the one-organization MVP.
- Choose the open-source license and contribution/governance model; the current request establishes open-source intent but not a license.
- Confirm how cost centers are assigned (repository metadata, policy mapping, ServiceNow catalog data, or another source) and which Actions billing data is available to the installation.
- Produce a capability matrix for each required operation, GitHub API, permission, plan requirement, and fallback before enabling mutations.

## Approval Gate

This document records the selected hybrid direction and the current scope proposal. Review the assumptions and open decisions above. An implementation plan should be written only after this design is approved; no product code or infrastructure changes are authorized by this document alone.

## Decision and Trackable Story Backlog

Decision update, 2026-11-05: The hybrid control-plane direction is approved for the product backlog. This update supersedes the earlier “Proposed for review” status above. Stories are grouped by delivery phase; implementation plans should be created per epic after the relevant open decisions and API capability checks are resolved. Priorities: P0 = foundation/MVP, P1 = next release, P2 = later release.

### Epic A: GitHub connection, jobs, approvals, and audit (Phase 1, P0)

**GOV-01: Register an organization integration**

As a GitHub owner, I want to connect an organization through an owner-installed GitHub App so that OctoCraft can read or perform only the operations I explicitly authorize.

Acceptance criteria:
- The setup flow lists each requested permission with its purpose before installation or permission changes.
- The connection shows organization identity, granted capabilities, last successful check, and revoked/invalid state.
- Missing permissions disable only dependent capabilities and explain the required owner action.

**GOV-02: Publish an operation capability catalog**

As an operator, I want each operation classified by API availability and granted authority so that users know what can be automated.

Acceptance criteria:
- Each operation is labeled executable, approval-required, administrator-action-required, or unsupported.
- The catalog includes required GitHub scope and any known plan, role, or API limitation.
- Execution rechecks capability and authorization instead of trusting an old request-time result.

**GOV-03: Submit a typed administration job**

As a requester without admin rights, I want to submit a structured job so that an authorized operator can review and execute it.

Acceptance criteria:
- A job has a stable ID, requester, operation type, typed inputs, target organization/repositories, status, timestamps, and source reference.
- Unsupported operation types and invalid targets are rejected before approval.
- Free-form text is treated as context, never as an instruction to run arbitrary commands.

**GOV-04: Approve or reject a proposed change**

As an approver, I want to review the exact proposed changes before execution so that privileged changes require accountable approval.

Acceptance criteria:
- The review shows targets, requested access/settings, policy evaluation, capability status, and before/after values when available.
- Approval is bound to the exact job and policy version; changing either invalidates the approval.
- Rejected jobs cannot execute and retain the approver and reason in their history.

**GOV-05: Maintain an auditable operation history**

As an auditor, I want to trace each request from source through execution so that administrative changes can be explained later.

Acceptance criteria:
- History records requester, source/ticket, approver, policy version, operation, target, timestamps, result, and GitHub response classification.
- Secrets and access tokens are redacted.
- Failed, retried, cancelled, and manually completed jobs remain distinguishable; records are not silently overwritten.

**GOV-06: Manage policies through CRUD**

As a platform owner, I want to create, view, update, and retire policy records so that approved templates, access levels, defaults, and approval rules are centrally managed.

Acceptance criteria:
- Policy changes are validated, versioned, attributable, and auditable.
- Jobs record the policy version used for evaluation.
- Invalid or conflicting policy updates are rejected with actionable validation errors.

### Epic B: Repository lifecycle and team access (Phase 1, P0)

**REP-01: Request a repository from an approved template**

As a team member, I want to request a repository from an approved template so that new projects start with consistent settings.

Acceptance criteria:
- The request captures a unique repository name, owner, template, visibility, description, team slugs, and cost center when configured as required.
- The requester sees validation failures before submitting for approval.
- A duplicate name or unavailable template cannot be approved as a valid create operation.

**REP-02: Provision a repository from the approved request**

As an approver, I want the approved repository request executed through GitHub so that repository creation is repeatable and auditable.

Acceptance criteria:
- Execution uses the approved template and settings snapshot, not mutable form values.
- The created repository is linked to the job and its resulting settings are recorded.
- Retries do not create a second repository; ambiguous outcomes require reconciliation before retry.

**REP-03: Map a team slug to repository access**

As a platform operator, I want to grant a selected GitHub team slug an approved role on a repository so that access can be delegated without giving the requester admin rights.

Acceptance criteria:
- The job verifies the team and repository belong to the connected organization and the requested role is allowed by policy.
- The access change requires the configured approval and integration capability.
- The result shows the team slug, repository, granted role, and any GitHub limitation or failure.

**REP-04: View effective repository access**

As an auditor or repository owner, I want to see people and teams with access to a repository so that direct and inherited access is understandable.

Acceptance criteria:
- The view distinguishes direct grants, team grants, and inherited grants where GitHub exposes that information.
- The view shows role/access level and last refresh time.
- Incomplete visibility is clearly labeled with the missing permission or API limitation; it is not shown as a complete audit.

**REP-05: Detect access and policy drift**

As a platform owner, I want to compare observed repository access with policy so that unexpected grants and missing required teams are visible.

Acceptance criteria:
- A scan reports unexpected grants, missing required grants, and policy/settings mismatches with repository and team/person context where available.
- The report identifies the policy version, scan time, and visibility limits.
- Phase 1 scans are read-only and never change GitHub state.

**REP-06: Propose a reviewable drift correction**

As an approver, I want a detected drift item converted into a normal proposed job so that remediation follows the same approval and audit controls.

Acceptance criteria:
- The proposed change contains the exact add/remove/update operation and affected target.
- The policy and GitHub capability are rechecked before approval and execution.
- No permission is removed or added automatically in Phase 1.

### Epic C: Organization and enterprise Actions policy (Phase 2, P1)

**ACT-01: Inspect Actions allowlist policy**

As a platform owner, I want to view the current organization/enterprise Actions policy and allowed external actions so that I can compare it with our approved policy.

Acceptance criteria:
- The view reports the source, scope, last refresh, and required access for the data.
- It distinguishes organization settings from enterprise settings.
- If the integration cannot read the setting, the UI explains that limitation rather than presenting an empty allowlist.

**ACT-02: Request an external Action allowlist change**

As a developer, I want to request an external Action to be added to the allowlist so that required automation can be reviewed without granting me enterprise admin access.

Acceptance criteria:
- The request identifies the Action owner/repository, pinned reference or version policy, justification, and affected organization scope.
- Policy checks flag unpinned references or disallowed sources before approval.
- The capability catalog determines whether OctoCraft can execute the change; otherwise it creates a clearly labeled administrator task.

**ACT-03: Apply or hand off an approved allowlist change**

As an enterprise administrator, I want an approved allowlist request applied when the GitHub API and granted authority permit it, or packaged as a precise manual task otherwise.

Acceptance criteria:
- Successful API changes record the resulting setting and GitHub response.
- Manual handoff includes exact navigation/API context and proposed value and remains pending until an administrator records completion.
- OctoCraft never marks a manual handoff complete merely because it was approved.

### Epic D: ServiceNow intake and scheduled execution (Phase 2, P1)

**SNOW-01: Configure a ServiceNow connection and schedule**

As an operator, I want to configure a ServiceNow instance, credentials reference, ticket filter, and polling schedule so that approved ticket types become OctoCraft jobs.

Acceptance criteria:
- Secrets are referenced from the configured secret backend and are never displayed after save.
- Polling interval, ticket query, and enabled operation types are explicit configuration.
- Connection health and last poll result are visible; disabled integration performs no polling.

**SNOW-02: Convert a matching ticket into a typed job**

As a requester, I want an eligible ServiceNow ticket converted into a validated job so that the ticket participates in the standard approval process.

Acceptance criteria:
- Only configured ticket types and fields map to allowlisted job types and typed parameters.
- Missing, malformed, or unauthorized fields produce a visible validation result and do not execute.
- The original ticket ID and link remain associated with the job.

**SNOW-03: Poll idempotently and update ticket status**

As an operator, I want scheduled polling to avoid duplicate jobs and reflect job outcomes in ServiceNow so that retries are safe and ticket owners can follow progress.

Acceptance criteria:
- Re-reading a ticket or retrying after a network failure does not create duplicate jobs.
- Polling uses a durable cursor or equivalent checkpoint, bounded retries, and visible failure state.
- Ticket status/comment updates are configurable and include the OctoCraft job link without disclosing secrets.

### Epic E: Policy reconciliation (Phase 3, P2)

**REC-01: Choose a policy enforcement mode**

As a policy owner, I want each policy set to run in report-only, propose-remediation, or explicitly enabled reconcile mode so that automation strength is deliberate.

Acceptance criteria:
- Report-only mode never mutates GitHub.
- Propose-remediation mode creates reviewable jobs but does not execute them without approval.
- Reconcile mode requires explicit owner authorization, documented scope, and an audit event before activation.

**REC-02: Pause and recover reconciliation safely**

As an operator, I want to pause a policy reconciler and inspect its last successful run so that failures or unexpected changes can be contained.

Acceptance criteria:
- Each run has start/end time, policy version, targets, changed resources, and result.
- Pause prevents new mutations while allowing in-flight jobs to reach a recorded terminal state.
- Failed or partial runs expose affected targets and a safe resume/retry path.

### Epic F: Repository backup and restore (Phase 3, P2)

**BACK-01: Configure repository backup policy**

As a repository owner, I want to select repositories, schedule, retention, and a supported storage destination so that backup coverage is explicit.

Acceptance criteria:
- Policy shows exactly which Git data and GitHub metadata are included and excluded.
- Storage encryption, retention, destination permissions, and last-run state are visible.
- Invalid credentials, insufficient storage permissions, or unsupported repository types block activation with a clear error.

**BACK-02: Run and verify a repository backup**

As an operator, I want scheduled backups checked for integrity so that I can trust the backup inventory.

Acceptance criteria:
- Each backup records repository, revision/time, destination, size, integrity result, and errors without logging secrets.
- Failed or incomplete backups are visible and retried according to bounded policy.
- A restore verification can confirm recoverability without overwriting a live repository.

### Epic G: Secret and cloud provider integrations (Phase 4, P2)

**INT-01: Store integration credentials in Vault**

As a security administrator, I want GitHub, ServiceNow, and storage credentials referenced from Vault so that OctoCraft does not become a competing secret store.

Acceptance criteria:
- Supported Vault configuration is validated before use and secret values are never returned to the UI or logs.
- Rotation/revocation failures are visible and disable affected operations safely.
- Existing GitHub App credential handoff behavior remains supported during migration.

**INT-02: Use Azure as an optional deployment/storage provider**

As a self-hosting operator, I want to configure Azure-backed services without making Azure mandatory so that organizations can deploy to their existing platform.

Acceptance criteria:
- Azure-specific setup is isolated behind documented provider interfaces and configuration.
- The core app and local development mode operate without Azure credentials.
- Identity, required roles, data location, and teardown/retention behavior are documented.

**INT-03: Use AWS as an optional deployment/storage provider**

As a self-hosting operator, I want to configure AWS-backed services without making AWS mandatory so that organizations can deploy to their existing platform.

Acceptance criteria:
- AWS-specific setup is isolated behind documented provider interfaces and configuration.
- The core app and local development mode operate without AWS credentials.
- Identity, least-privilege permissions, data location, and teardown/retention behavior are documented.

### Epic H: GitHub Actions analytics and cost centers (Phase 4, P2)

**AN-01: View Actions usage by repository**

As a platform owner, I want to see available Actions usage data by repository so that unusually high usage can be investigated.

Acceptance criteria:
- Metrics show source, time range, collection time, and any missing repositories/data.
- Repository-level values are reported only when GitHub provides them or a documented attribution method is used.
- Filters support organization, repository, and time range.

**AN-02: Assign repositories to cost centers**

As a finance or platform operator, I want repositories mapped to cost centers so that usage can be grouped for internal reporting.

Acceptance criteria:
- Cost-center mappings have an owner, source, effective dates, and audit history.
- Unmapped or multiply mapped repositories are called out instead of silently allocated.
- Reports distinguish measured GitHub charges from internal estimates or allocations.

**AN-03: Review organization platform health**

As a platform lead, I want one dashboard for access drift, policy compliance, backups, ownership, and Actions usage so that I can prioritize operational risks.

Acceptance criteria:
- Each metric links to its underlying repositories, jobs, or policy findings.
- Data freshness and unavailable integrations are visible on the dashboard.
- Dashboard totals do not imply coverage when permissions or API access are incomplete.

### Epic I: Open-source self-hosting and operations (Phase 1 foundation, P0)

**OSS-01: Run the core platform without a cloud provider**

As a community operator, I want a documented local/self-hosted mode so that evaluation and core administration workflows do not require Azure or AWS.

Acceptance criteria:
- Setup documents prerequisites, local configuration, data persistence, and how to stop/remove local state.
- Optional provider integrations can be disabled independently.
- The documented local mode does not imply production authentication or exposure safety beyond its actual configuration.

**OSS-02: Understand installation permissions and security boundaries**

As a GitHub owner, I want an up-to-date permission matrix and threat model so that I can make an informed decision before installing the app.

Acceptance criteria:
- Documentation maps each capability to GitHub permissions, scope (repo/org/enterprise), plan/API restrictions, and manual fallback.
- Credential storage, ticket input trust, approval requirements, audit retention, and uninstall/revocation behavior are documented.
- The project identifies which features are experimental and which have production recovery procedures.

**OSS-03: Contribute and operate the open-source project**

As a contributor or self-hosting operator, I want clear license, contribution, upgrade, and backup/restore guidance so that I can adopt and maintain the project responsibly.

Acceptance criteria:
- The repository declares an approved open-source license and contribution process before public release.
- Upgrade, configuration migration, data export, and recovery procedures are documented.
- Supported integrations and tested deployment modes are listed with known limitations.

## Story Backlog Notes

- Story IDs are stable tracking keys; status, owner, target release, and issue/PR links can be added as implementation begins.
- Stories are product outcomes, not a substitute for the smaller implementation plans required for each epic.
- Phase 1 stories should be split into independently testable implementation plans. Phase 2-4 epics remain roadmap candidates until API, ServiceNow, backup scope, licensing, and cost-data questions are resolved.
- GitHub Actions enterprise allowlist operations, full access visibility, billing attribution, and backup metadata coverage must be validated against the target GitHub plan and APIs before promising them as executable.