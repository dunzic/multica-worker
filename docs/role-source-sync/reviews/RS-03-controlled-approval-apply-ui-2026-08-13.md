# RS-03 controlled approval and apply UI review

Date: 2026-08-13

Decision: **GO for merge behind the independent default-off apply flag; NO-GO for broad production enablement**

## Four-party score

| Reviewer | Score | Decision evidence |
| --- | --- | --- |
| Architecture | 2/3 | The shared Web/Desktop view uses the source-neutral Core API, preserves backend owner/admin enforcement, binds plan creation to the latest successful snapshot, canonicalizes every archive decision and relies on the existing transactional CAS/receipt contract. Secret-transfer orchestration and live commit/failover evidence remain open. |
| Product | 2/3 | Operators now have one understandable sequence: scan, generate plan, review impact, decide every archive, approve the exact digest, confirm again, apply and inspect the receipt. There is no guided secret-transfer or recovery workflow yet. |
| Test | 2/3 | Component tests cover the independent feature gate, latest-snapshot target, exhaustive decisions, canonical ordering, second confirmation and ambiguous-response idempotency. Typecheck and the Views Vitest workspace pass. Browser accessibility, two-operator races and live PostgreSQL/S3/Kubernetes faults remain unproven. |
| CEO | 2/3 | This closes a visible enterprise upgrade workflow and materially improves differentiation, but enabling it before operational rehearsal would create outsized trust and support risk. |

## What changed

- `role_source_apply` remains independently default-off. With it disabled, members and administrators retain the audit-only surface.
- With it enabled, only workspace `owner` and `admin` users see mutation controls; the server independently enforces the same role boundary.
- Plan generation accepts only the digest from the latest successful persisted scan shown to the operator.
- Every `archive_candidate` starts unset. The operator must explicitly select `retain` or `archive`; the submitted list is canonical and bound to the plan contract and digest.
- Approval is a separate durable action. Apply stays disabled until an approved record for the exact current plan is visible.
- Apply requires a second confirmation that restates the digest and create/update/archive counts.
- Successful applies are displayed as content-addressed receipts and counts. An unverifiable response does not become UI success.
- Ambiguous network/response failures retain the same approval or apply idempotency key; explicit server errors clear it for a corrected attempt.

## Open objections

1. A plan whose roles require environment or MCP material needs one-time secret transfers. The backend fails closed, but this UI does not yet request, monitor or map those transfers, so that class of plan is not an end-to-end operator workflow.
2. Failed-attempt evidence is visible, but recovery is still runbook/API driven. There is no reconcile-first guided recovery control.
3. Current evidence is static/unit/in-process. No live PostgreSQL duplicate-key, commit-timeout, failover, 10,000-user, object-store or Kubernetes failure exercise was available locally.
4. No browser-based keyboard/screen-reader or two-operator approval/apply rehearsal has been recorded.

## Production gates

- Build the redacted secret-transfer request/status workflow and prove exact role-to-transfer mapping.
- Rehearse plan/approval/apply/ambiguous response/reconcile/retry with two operators against staging PostgreSQL and object storage.
- Inject failure before, during and after commit and record receipt/failure-ledger reconciliation outcomes.
- Run candidate-image 10,000-user load, lock/WAL/latency measurements and Kubernetes restart/failover tests.
- Complete keyboard, screen-reader and destructive-action usability testing.

Until those gates pass, keep `role_source_apply=false` for broad cohorts.
