# RS-06 permanent-purge ambiguity evidence review — 2026-08-15

Scope: provider partial success, response loss, process interruption and the
truthfulness of immutable artifact-purge receipts

Execution record:
[`RS-06-purge-ambiguity-local-validation-2026-08-15.md`](../evidence/RS-06-purge-ambiguity-local-validation-2026-08-15.md)

Decision: **GO to merge behind the existing default-off artifact-GC gate;
NO-GO for a customer deletion cohort until candidate-store, two-replica,
primary-failover and restore evidence pass.**

## Value hypothesis

A provider may delete versions and then lose the response. Retrying can observe
an empty key, but that later observation cannot reconstruct the number of
versions or bytes deleted by the ambiguous attempt. Treating the retry's zero
counts as complete would create a cryptographically valid but semantically
false audit statement. This change preserves the stronger fact—final exact-key
absence—while making the weaker provider-operation evidence visibly incomplete.

## Architecture expert — 2/3

- Storage failures use a typed `MayHaveMutated` contract. Preflight failures
  remain non-mutating; delete transport/output errors, post-delete inventory
  failures and non-empty final inventory are ambiguous.
- Ambiguity is persisted on the leased deletion intent before retry. Reclaiming
  an expired `deleting` lease increments the same counter, conservatively
  covering process death after a provider mutation and before database commit.
- New immutable receipts use v2 and commit the ambiguity count plus
  `provider_evidence_complete`. Exact-key absence and logical bytes remain
  authoritative; version, delete-marker and observed-byte totals are lower
  bounds whenever ambiguity is nonzero. V1 digest verification remains byte
  compatible, and v2 rows block destructive schema downgrade.
- API, Core schema, owner UI and DR verifier enforce the same invariant. A
  low-cardinality alert fires on any new ambiguous provider evidence.
- Remaining objection: local execution cannot prove two replicas, primary
  failover, candidate-provider request correlation or long-term receipt
  partition/retention behavior.

## Product expert — 2/3

- Owners see a fleet warning and each receipt labels counts as complete or
  lower bounds. The wording preserves useful deletion evidence without turning
  uncertainty into a storage-savings claim.
- No operator can edit the ambiguity counter, completeness flag or immutable
  receipt. Recovery converges through ordinary reconciliation, not a manual
  “mark complete” action.
- Existing access remains owner-only and details remain content-free.
- Remaining objection: controlled-cohort comprehension, export language,
  evidence-retention terms and support playbooks are not yet validated.

## Test expert — 2/3

- S3 unit tests distinguish preflight failure from partial delete and simulate
  a timeout after the provider has mutated with SDK retries disabled. A later
  retry proves empty inventory without claiming the lost counts.
- Reconciler and receipt tests cover result-preserving ambiguous failures,
  deterministic v2 commitments, tamper rejection, invariant violations and
  exact v1 compatibility.
- A fresh PostgreSQL 17 database migrated from 001 through 387. Three
  consecutive runs passed ordinary five-pass completion, response-loss retry
  and expired-lease reclaim. Direct SQL showed three complete v2 receipts,
  three incomplete v2 receipts, all exact keys absent, zero residual intents
  and the immutable trigger enabled.
- Downgrade with v2 receipts failed with the intended exception. Migration 387
  down/up on an empty ledger removed and restored all four fields.
- Remaining objection: candidate S3 process kill, two-replica race, primary
  failover, restore and large-version/backlog SLO evidence are external gates.

## CEO / rollout owner — 2/3

- The change prevents an auditable but misleading deletion statement, reducing
  enterprise trust, compliance and support risk across every adapter.
- Default-off deletion and explicit lower-bound language avoid overstating ROI
  or provider billing impact.
- A score of 3 would require a named RACI, measured candidate-topology failure
  rate and recovery time, provider reconciliation labor/cost, and a signed
  restore exercise with RPO/RTO.

## Merge and production gates

Merge is permitted with `MULTICA_ROLE_SOURCE_ARTIFACT_GC_ENABLED=false`.
Production remains NO-GO until all of the following are recorded:

1. two replicas plus PostgreSQL primary failover during provider mutation and
   final receipt commit;
2. candidate versioned store partial response, timeout, process kill, late PUT,
   Object Lock and version-delete denial, with independent request/inventory
   evidence;
3. Gate F restore verifying v1/v2 commitments and preserving incomplete
   evidence labels, with measured RPO/RTO;
4. alert, quarantine, provider-reconciliation and rollback rehearsals owned by
   named SRE, security, product and data-retention roles; and
5. no deletion-savings claim until independent inventory and billing evidence
   agree after the provider accounting delay.
