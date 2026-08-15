# RS-06 disaster recovery review — 2026-08-13

Feature: source history, artifact and secret-transfer recovery
Gate: design / merge
Final decision: CONDITIONAL; merge behind disabled deletion gates, NO-GO for a
production cohort until Gate F runs on the candidate topology.

| Perspective | Score | Decision and open objections |
| --- | ---: | --- |
| Architecture expert | 2/3 | One exported PostgreSQL snapshot, deterministic artifact archive, stable exclusive/shared lock protocol, independently trusted Ed25519 manifest, source-neutral verifier and no-FK relationship audit close the main split-backup gap. The packaged local signed dump/restore/artifact gate now passes; managed failover/lock behavior and approved KMS escrow integration for signing and secret-transfer keys remain open. |
| Product expert | 2/3 | Operators get three explicit steps and a redacted pass/fail report; historical secrets are correctly described as non-restorable. A hosted control-plane UI and managed drill scheduling are intentionally absent from this operator slice. |
| Test expert | 2/3 | Unit/static, digest-tamper, archive round-trip, compile, race and opt-in live gates exist. Three packaged PostgreSQL 17 dump/restore runs now pass with a non-empty object inventory and four fail-closed corruption classes. Versioned S3, interrupted copy/restore, primary failover, key-loss and large-inventory evidence remain open. |
| CEO | 2/3 | Auditable recovery materially reduces enterprise adoption risk and avoids claiming that a DB snapshot is sufficient. RTO/RPO, storage cost and drill labor have not yet been measured for pricing/SLA commitments. |

Security and data-loss blockers:

- private keys must be escrowed outside the backup and supplied only to the
  isolated verifier; the report exposes IDs/counts, never key bytes;
- the backup directory is exclusive/private and outputs are never overwritten;
- any mismatch, missing object, invalid digest/chain/edge or key makes the
  verifier non-zero; no sampling is used;
- `pg_dump`, `pg_restore`, database and object-store transport encryption and
  immutable off-site storage remain operator/platform responsibilities.

Rollout decision: keep `MULTICA_ROLE_SOURCE_RETENTION_ENABLED` and
`MULTICA_ROLE_SOURCE_ARTIFACT_GC_ENABLED` false until a named SRE, security
approver and product owner sign a successful Gate F exercise with RPO/RTO,
failure injection and restore evidence.

## 2026-08-15 local execution addendum

The packaged local gate now creates fully migrated PostgreSQL 17 source and
restore databases, one 38-byte immutable artifact, a signed manifest and fresh
object directory. Three consecutive runs plus one final smoke run pass dump,
restore, double artifact rehydration, all-25-table verification and independent
digest/row checks. Every run rejects archive tamper, missing object, changed
object and changed database state; a failed `pg_dump` retains `INCOMPLETE` and
never writes a manifest. The three formal bundles were 437,776–437,844 bytes,
restore plus first full verification took 2 coarse wall-clock seconds, and the
complete fault matrix took 9–10 seconds.

The first executions found and closed two packaged-command defects: discarded
`pg_dump` diagnostics and failure under an arbitrary numeric container UID.
The command now returns at most 2 KiB of subprocess diagnostics, passes a
password-free URL/explicit database username to `pg_dump`, keeps the password
only in a deduplicated `PGPASSWORD` environment entry and strips pgx-only pool
parameters. See
[RS-06 local signed backup/restore drill review](./RS-06-local-restore-drill-2026-08-15.md).

This raises the local test contract to 3/3 but does not change the overall 2/3
or production NO-GO: the fixture is tiny and local, with no managed failover,
versioned provider, KMS/HSM, concurrent destructive traffic or measured
production RPO/RTO.
