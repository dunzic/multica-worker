# RS-02 operator read-only scan review

Date: 2026-08-13

Gate: implementation and merge evidence

Final decision: **GO to merge behind the existing default-off sync/scan flags;
NO-GO for broad production until live daemon, PostgreSQL and operator rehearsal
evidence passes.**

## Customer and product outcome

A workspace owner or admin can queue the safest useful role-source operation:
a read-only scan. The settings view survives refresh and operator handoff by
loading the durable latest scan, polls only while work is queued or claimed,
then shows a redacted terminal status, timestamps, snapshot commitment or
stable error code. Members may inspect that evidence. No approval, apply,
retry, recovery or configuration authority was added.

## Architecture review — 2/3

- The latest read is tenant- and source-scoped and uses the existing
  `(workspace_id, source_id, requested_at DESC, id)` listing index.
- The response DTO excludes requester, runtime identity, lease token, lease
  expiry, paths, configuration and provider errors.
- The existing partial unique index remains the concurrency authority for one
  queued/claimed scan per source. Stable conflict codes let clients reconcile
  rather than pattern-match English text.
- A client request key is stored only as a SHA-256 commitment under a
  source-scoped concurrent unique index. Ambiguous retries reuse the key,
  return the exact existing scan and do not wake the daemon twice; legacy
  empty-body clients receive a bounded server-generated key.
- Paused and detached sources fail with a typed conflict; the backend remains
  authoritative even when an older client enables its button incorrectly.
- Open objection: latest-only visibility is sufficient for routine handoff but
  not a full incident timeline; realtime push would reduce polling at fleet
  scale.

## Product review — 2/3

- The UI states that a successful scan creates evidence and a proposal, not an
  applied change. This removes the most dangerous operator ambiguity.
- Owner/admin can request; members can inspect. Active work disables duplicate
  submission, while refresh restores the server-owned status.
- Stable error codes are visible without leaking infrastructure diagnostics.
- Open objection: production rollout still needs guided runtime/source
  selection, scan-history drill-down, localized remediation for the complete
  failure taxonomy and an approved escalation path.

## Test review — 2/3

- Handler tests cover empty history, tenant-scoped DTO redaction, active-scan
  and invalid-source-state conflicts with stable codes, idempotent replay
  without duplicate runtime wakeup, and legacy empty-body compatibility.
- API tests cover valid GET/POST responses, 404-as-empty and malformed-response
  fail-safe parsing for old/new desktop compatibility.
- UI tests cover owner/admin request authority, member read-only visibility,
  active-scan disabling, ambiguous-failure key reuse and continued absence of
  approval/apply/retry/recovery.
- Focused handler and role-source tests pass under the race detector; Core and
  Views suites pass.
- Open objection: no live daemon claim/renew/report, browser refresh during
  failover, two-operator race, 10,000-user poll load or accessibility rehearsal
  has been recorded.

## CEO review — 2/3

- This closes the minimum operational loop required for a controlled pilot:
  an authorized person can request fresh evidence and hand it to a reviewer
  without shell access or mutation authority.
- It reduces support dependence and accidental-apply risk while reusing the
  generic role-source control plane, so the value applies to AgentWaker and
  future adapters rather than creating a fork.
- Open objection: commercial value still depends on measured time-to-diagnose,
  operator error reduction, successful pilot scans and support-ticket savings.

## Evidence required for 3/3

1. Staging exercise with two operators and a real daemon covering queued,
   claimed, succeeded, failed, cancelled, refresh, handoff and duplicate click.
2. PostgreSQL primary failover during latest-read and claim/report, proving no
   false success, lost terminal state or cross-tenant result.
3. 10,000-user candidate load showing bounded API/database/Prometheus impact;
   compare two-second polling with a future event-driven update.
4. Product/SRE rehearsal with named escalation owner, localized failure
   remediation, accessibility checks and evidence that users do not interpret
   scan success as apply success.
