# RS-03 failed-apply recovery guidance review

Date: 2026-08-13

Final decision: **GO to merge behind controlled apply; production remains
conditional on failure injection and a recorded two-operator recovery drill.**

## Architecture review — 3/3

The operator surface does not add a privileged retry endpoint or reuse a stale
request key. It refreshes source, latest scan, plans, successful receipts and
append-only failure evidence through existing tenant-scoped read APIs. Any
subsequent apply must still traverse the normal current-snapshot CAS, exact
plan approval, capacity, secret-transfer, idempotency and second-confirmation
gates. Commit-stage errors are explicitly treated as ambiguous until reconciled
against verified successful receipts.

## Product review — 2/3

Stable failure codes now produce actionable, non-technical recovery guidance:
refresh secret transfers, restore capacity/dependencies and return to a still
current approved plan, or rescan/rebuild after state and policy conflicts. A
single `Refresh evidence` action reconciles all relevant views, and only a
failure for the current plan links back to that plan. There is deliberately no
`Retry apply` button beside a historical failure.

Open objection: a dedicated incident timeline, assignee/acknowledgement state,
support bundle export and guided source restoration remain future work.

## Test review — 2/3

Component tests prove recovery text, absence of a direct retry action, current-
plan navigation and the exact evidence query families refreshed. Typecheck and
full view tests pass. Missing: injected commit timeout/restart and operator drill
showing time-to-diagnose and safe resolution.

## CEO review — 2/3

This closes a visible supportability gap and reduces the chance that an
operator doubles a destructive apply after an ambiguous timeout. It is strong
pilot value, but production claims still require live failure and recovery
evidence.
