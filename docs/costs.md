# Cost model and billing drivers

**Reference date: 2026-11-04. Currency: USD. Region: East US 2.** Azure runtime is currently deleted; the app has no currently provisioned compute/storage/logging meters. Prior consumption can still appear on a later bill. GitHub plan seats are independent of Azure teardown.

This is a planning model, **not a quote or spending guarantee**. Contract discounts, credits, subscription eligibility, taxes, exchange rates, shared free grants and different regions can change the invoice.

## Rates and component purposes

These reference rates were checked against Microsoft’s [Retail Prices API](https://learn.microsoft.com/en-us/rest/api/cost-management/retail-prices/azure-retail-prices) for `armRegionName=eastus2`, Consumption pricing.

| Component / configuration | What drives billing | Reference retail rate / estimate |
|---|---|---|
| Functions Flex **on demand**, Node 22, 2 GB, no always ready | Active instance memory-time in GB-seconds + executions; concurrent requests share instance time | $0.000026/GB-s and $0.40/million executions; eligible paid subscriptions get 100,000 GB-s + 250,000 executions monthly across Flex on-demand apps |
| FC1 plan | Hosting configuration, not a fixed reserved server | No separate always-on base fee in this configuration; execution meters above |
| Blob Hot LRS, GPv2 | Average GB stored, write/read/list transactions, downloads/egress, retained deployments | First capacity band $0.0184/GB-month; writes $0.05/10k; reads $0.004/10k reference |
| Table Standard LRS | Average GB of manifests/history + read/write/query transactions | About $0.045/GB-month and $0.00036/10k standard transactions; account-encrypted table products have different operation rates and must not be substituted |
| Host storage/queues | Runtime locks, host metadata, transactions/capacity | Additional small Storage usage; not included in the request-row-only calculation |
| Key Vault Standard **secret operations** | Secret write/read/list/version operations and Key Vault reference refreshes | $0.03/10k operations; no HSM pool or per-key HSM monthly meter in this design |
| Application Insights + Log Analytics | Ingested telemetry volume, retention beyond allowance, export/query features if enabled | Analytics log ingestion $2.76/GB, excess retention reference $0.12/GB-month; telemetry is not billed twice for linking Insights to its workspace |
| Entra registrations / managed identity / OIDC | Identity configuration | No per-registration/runtime authentication meter assumed; premium Entra policies/Conditional Access may require paid licenses |
| GitHub Actions | Runner minutes, artifact GB-hours, included allowances and purchased plan | Validate on GitHub billing page; no claim that all private-repo minutes/artifacts are free |
| GitHub private repo deployment review | Organization plan/user seats | Required environment reviewers on private repos need supported Enterprise features; budget seats/contract separately |
| External HashiCorp Vault | Existing platform/cloud machines/license or managed service | Not deployed here, no universal rate; organization platform costs extra |
| Email delivery | Future provider messages/data/domain setup | $0 today in this app because no mail service is integrated; future ACS/SMTP cost is extra |
| Terraform local state | Local disk | No cloud meter; sensitive, single-operator POC storage |
| Optional remote state | Separate state Blob capacity/operations, retained versions | Extra storage outside this template; usually small but not free by definition |

**Do not use the classic Consumption free grant** (1M executions / 400k GB-s) for this Flex deployment. Flex’s on-demand grant is smaller. Always-ready baseline and execution meters have different pricing and do not receive the on-demand free grant.

## How to calculate Functions

For sequential requests where one instance runs one request at a time:

```text
memory_time ≈ executions × mean_active_seconds × configured_memory_GB
compute_cost = max(0, memory_time - available_GB_seconds_grant) × 0.000026
execution_cost = max(0, executions - available_execution_grant) / 1,000,000 × 0.40
```

At concurrency, Azure meters instance active memory-time, not a naïve sum of overlapping request durations. Use measured billing/telemetry for a production forecast. Cold starts, SDK calls, retries and Vault latency can change active time.

## Example A: low-volume POC

Assumptions per month:

- 100 app requests, 50,000 HTTP executions including UI polling, mean active time 0.2 seconds, 2 GB configured memory.
- 1 GB average Blob data/packages, 0.1 GB Table data.
- 10,000 Blob writes and 10,000 reads; 100,000 Table operations.
- 2,000 Key Vault operations; 1 GB log ingestion.
- No always ready, paid network features, email provider or external Vault bill.

| Meter | Calculation | USD |
|---|---|---|
| Flex compute | 50,000 × 0.2 × 2 = 20,000 GB-s, within unused eligible grant | $0.00 |
| Flex executions | 50,000, within unused eligible grant | $0.00 |
| Blob capacity/operations | 1 × .0184 + 1 × .05 + 1 × .004 | $0.0724 |
| Table | .1 × .045 + 10 × .00036 | $0.0081 |
| Vault | 2,000 / 10,000 × .03 | $0.0060 |
| Logs, conservatively ignoring free allowances | 1 × 2.76 | $2.76 |
| Illustrative subtotal | Above meters only | **$2.85/month** |

If the Flex free grants are already exhausted or unavailable, compute/executions add approximately $0.54 for these assumptions. A **$3-10/month Azure POC planning range** provides some headroom but is not a cap; excludes seats, egress, extra storage and any paid add-ons.

## Example B: busier portal

Assumptions: 2M HTTP executions, 0.3 seconds mean sequential active time, 2 GB, 10 GB Blob, 1 GB Table, 100k Blob writes/reads, 1M Table operations, 50k Vault operations, 10 GB logs. Available Flex grants assumed unused by other apps.

| Meter | Calculation | USD |
|---|---|---|
| Flex active memory | (1,200,000 − 100,000) × .000026 | $28.60 |
| Flex executions | (2M − .25M) / 1M × .40 | $0.70 |
| Blob | 10 × .0184 + 10 × .05 + 10 × .004 | $0.724 |
| Table | 1 × .045 + 100 × .00036 | $0.081 |
| Vault | 5 × .03 | $0.15 |
| Logs | 10 × 2.76 | $27.60 |
| Illustrative subtotal | Excludes network/seats/add-ons | **$57.86/month** |

10 GB of logs exceeds the configured 0.1 GB/day POC ingestion cap if spread evenly; budget this example only if raising that cap. The cap is delayed and **not** a guaranteed hard limit, and it can cause missing diagnostic data. This example illustrates why compute/logging can dominate tiny manifest storage.

## Polling matters more than number of created apps

One idle browser polling every ten seconds for an eight-hour workday creates about **2,880 list calls/day**. Twenty workdays give 57,600 calls per browser. Ten approvers can generate over half a million list executions/month without creating a single app. Each full table query can make multiple backend transactions as rows/pages grow.

Budget **API executions and storage operations**, not just GitHub App count. App creation itself has no per-app GitHub creation charge in this model.

## Cost controls

1. Keep always-ready instances absent; scale to zero when inactive.
2. `maximum_instance_count=40` is a concurrency ceiling, **not** a billing limit.
3. Set Azure Cost Management budgets and alerts (e.g. $10 POC warning); budgets notify, they do not automatically stop resources.
4. Log sampling is enabled in `host.json`; requests are excluded from sampling, so busy UI polling still emits request telemetry.
5. Monitor the daily ingestion cap and missing logs; increase deliberately, not silently.
6. Archive old requests, expire credential outbox ZIPs, and retain a limited number of deployment packages. Automatic lifecycle expiration is **not implemented yet**.
7. Prefer Vault; secret-bearing ZIP retention increases risk and storage.
8. Choose Hot LRS for small frequent-use objects; cooler tiers bring retrieval/early deletion fees.
9. Destroy through Terraform when done, keeping state/identity plans separate.

## Verify your own estimate

Use the [Azure pricing calculator](https://azure.microsoft.com/en-us/pricing/calculator/), selecting Flex Consumption, East US 2, standard Hot LRS, Standard Key Vault, and workspace-based monitoring. Check the actual Azure subscription billing data after deployment.

Sources:

- [Functions pricing and Flex free grants](https://azure.microsoft.com/en-us/pricing/details/functions/)
- [Blob pricing](https://azure.microsoft.com/en-us/pricing/details/storage/blobs/)
- [Table pricing](https://azure.microsoft.com/en-us/pricing/details/storage/tables/)
- [Key Vault pricing](https://azure.microsoft.com/en-us/pricing/details/key-vault/)
- [Azure Monitor pricing](https://azure.microsoft.com/en-us/pricing/details/monitor/)
- [GitHub environment feature availability](https://docs.github.com/en/actions/reference/workflows-and-actions/deployments-and-environments)
- [GitHub Actions billing](https://docs.github.com/en/billing/concepts/product-billing/github-actions)
