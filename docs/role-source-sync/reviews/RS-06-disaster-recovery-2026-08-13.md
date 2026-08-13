# RS-06 disaster recovery review — 2026-08-13

Feature: source history, artifact and secret-transfer recovery
Gate: design / merge
Final decision: CONDITIONAL; merge behind disabled deletion gates, NO-GO for a
production cohort until Gate F runs on the candidate topology.

| Perspective | Score | Decision and open objections |
| --- | ---: | --- |
| Architecture expert | 2/3 | One exported PostgreSQL snapshot, deterministic artifact archive, stable exclusive/shared lock protocol, independently trusted Ed25519 manifest, source-neutral verifier and no-FK relationship audit close the main split-backup gap. Still requires measured failover/lock behavior and approved KMS escrow integration for signing and secret-transfer keys. |
| Product expert | 2/3 | Operators get three explicit steps and a redacted pass/fail report; historical secrets are correctly described as non-restorable. A hosted control-plane UI and managed drill scheduling are intentionally absent from this operator slice. |
| Test expert | 2/3 | Unit/static, digest-tamper, archive round-trip, compile, race and opt-in live gates exist. Real PostgreSQL 17 dump/restore, versioned S3, interrupted upload, primary failover and large inventory evidence remain open. |
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
