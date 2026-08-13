# Role-source runtime and attestation recovery runbook

This runbook restores the control plane without modifying role-source tables by
hand. Use an administrator account for the affected workspace. Never paste the
daemon's private config file, digest key, environment values or MCP credentials
into tickets, chat or logs.

## Alert: `MulticaRoleSourceRuntimeUnavailable`

The alert means at least one `registered`, `active` or `error` role source has
remained bound to a missing, offline or database-stale runtime for ten minutes.
The metric is deliberately fleet-level and contains no workspace, source or
runtime identifiers.

1. Check `MulticaBusinessSamplerQueryErrors` first. If query
   `role_source_runtime_availability` is failing, treat the availability gauge
   as stale and repair the metrics read pool or PostgreSQL before diagnosing a
   daemon.
2. In the authenticated workspace settings, open **Role sources** and filter
   for **Runtime offline or stale**. Record the workspace, source name, source
   state, runtime status, last evidence status and last observation time in the
   incident record. Do not copy opaque digests unless the incident store is
   approved for audit identifiers.
3. Confirm whether the bound daemon process is running and can reach the same
   Multica server and PostgreSQL-backed control plane. Check server and daemon
   logs for heartbeat authentication, websocket reconnect or attestation
   persistence errors. Do not infer health from a running process alone.
4. On the daemon host, as the same operating-system account that owns the
   daemon, inspect only the redacted local summary:

   ```bash
   multica daemon role-source show --output json
   ```

   Confirm that the expected config entry exists and that its adapter kind and
   version match the server source. Do not read or copy the private managed file
   directly.
5. Inspect the daemon's local `/health` response. Its
   `role_source_config.status` must be `loaded`. After an approved CAS `apply`,
   allow at least one five-second reload interval and verify that the revision
   and `last_successful_at` advance. If status is `degraded`, use only the
   bounded `error_code` and redacted `show` output to repair the file; the
   daemon is intentionally retaining its last-known-good generation. Restart
   only if the reload loop itself is not advancing or the process/runtime is
   unavailable.
6. Verify all of the following before resolving the incident:

   - the source's effective runtime-config status is `loaded`;
   - its preserved attestation status is `loaded`;
   - its runtime status is `online`;
   - the last observation advances after hot reload or recovery;
   - `multica_role_source_runtime_availability{status="runtime_unavailable"}`
     falls by the expected count and the alert clears after its hold period;
   - a read-only scan completes before any apply operation is considered.

If the daemon is intentionally retired, do not update PostgreSQL manually. An
owner/admin must first **Pause** the source, verify pending work is cancelled,
then **Detach** it. To migrate, **Rebind** the detached source to the reviewed
destination runtime and daemon-local config handle. Rebind deliberately leaves
the source paused; obtain fresh matching loaded evidence and complete a
read-only scan before **Resume**. A stale version or invalid transition must be
refreshed and reviewed, never forced.

## Legal-hold operations

Only a workspace owner may create or release a role-source legal hold. Use the
**Role sources → Legal holds** panel; do not insert, update or delete hold rows
manually.

1. Keep the case narrative and authority document in the approved external
   investigation/legal system. Select one closed Multica reason code.
2. Prefer a source-scoped hold when present and future snapshots must remain
   protected. Use snapshot scope only for an exact existing SHA-256 snapshot
   digest.
3. If correlation is required, submit an approved high-entropy or HMAC-derived
   SHA-256 commitment. Do not submit a predictable case number, customer name,
   email address or free text.
4. Confirm the hold appears as **Active hold** before relying on it. An active
   hold blocks workspace deletion and future historical pruning; it does not
   pause scans, apply changes or worker execution.
5. Release only after the external authority permits it. Select a closed
   release reason and retain that approval externally. Release is append-only
   and does not immediately delete evidence.

If workspace deletion returns `409` for active legal holds, stop the deletion
workflow and contact the case owner. Never delete the hold or release row in
the database. For an unexpected database mutation-guard error after all holds
show released, preserve the transaction error code, commit, workspace ID and
redacted hold IDs; escalate to engineering without disabling triggers.

## Historical-retention operations

The owner-only retention panel defaults to disabled. A policy revision must keep
snapshots for at least 30 days and reserve at least two distinct successfully
applied versions; the recommended initial cohort setting is 90 days and 10
versions. The preview counts referenced bytes, not uniquely reclaimable storage,
so do not use it as a savings invoice.

Before enabling, verify both server gates are intentionally configured:
`MULTICA_ROLE_SOURCE_RETENTION_ENABLED=true` and
`MULTICA_ROLE_SOURCE_ARTIFACT_GC_ENABLED=true`. The first removes eligible
snapshot content; the second permanently purges bodies after their final
reachability edge disappears. Enabling only the policy while the server gate is
off is a safe preview-only state.

Do not bypass a blocked candidate. `legal_hold`, `task_pin`, `object_mapping`,
`active_transfer`, `active_apply`, `recent_plan`, `rollback_reserve`,
`policy_age` and `policy_disabled` are expected safe deferrals. Investigate
`snapshot_missing`, `state_conflict` or `internal_failure`; preserve metrics,
bounded logs and audit events, then stop the worker gate if failures repeat.
Never set `multica.role_source_retention_prune` manually or disable the snapshot
and task-pin guards.

For rollback, confirm the target still appears in snapshot history before
approving a plan. Versions outside the configured reserve may have only their
digest/receipt/audit evidence left and are intentionally no longer runnable.
Before broad rollout, complete the PostgreSQL race, object-storage purge and
backup/restore gates in `production-validation.md`.

## Artifact-integrity quarantine operations

The integrity worker is independently default-off. Enable
`MULTICA_ROLE_SOURCE_ARTIFACT_INTEGRITY_ENABLED=true` only after the object
store read identity, request limits, alerts and this response path have been
tested in the target environment. The worker verifies current bytes; it never
deletes or overwrites an object.

`MulticaRoleSourceArtifactIntegrityQuarantined` means at least one body is
confirmed missing, the wrong size, or the wrong SHA-256. Snapshot and apply
fail closed for that digest. `MulticaRoleSourceArtifactIntegrityReadFailures`
means repeated transient read/open/close failures; those rows remain retryable
and are not quarantined. `MulticaRoleSourceArtifactIntegrityWorkerFailures`
means database claim/state/count progress failed, so fleet gauges may be stale.

1. Check object-store availability, credentials, throttling and version state.
   Do not copy an object key, digest, source path or provider error into an
   ordinary ticket or metric label.
2. Pause apply for the affected operational cohort while quarantine is nonzero.
   Existing materialized workers keep their pinned bytes; do not infer that a
   new scan is safe merely because the source is online.
3. Trigger an authorized read-only source scan. Missing-artifact preflight will
   request the exact digest from the owning daemon, which reopens the bounded
   source file and uploads only after revalidating size and SHA-256.
4. Never clear `role_source_artifact_integrity` manually and never copy an
   arbitrary object into the deterministic key. A valid exact upload is the
   only ordinary repair path; it records `reuploaded`, increments the repair
   count and returns the row to immediate verification.
5. Resolve the incident only after the quarantine fleet count falls, the body
   reaches `healthy`, a new read-only scan succeeds, and no read-failure alert
   remains. A transient recovery without healthy readback is not repair proof.

If quarantine grows across unrelated workspaces, disable the integrity worker
gate, preserve database and object-store audit evidence, and treat it as a
shared storage or integrity incident. Do not initiate bulk re-upload or GC. A
restore is acceptable only through the isolated DR workflow and its semantic
verifier; restoring PostgreSQL metadata alone cannot restore missing bodies.

## Attestation status recovery

The effective status and last evidence status answer different questions. A
runtime can be unavailable while its last valid evidence remains `loaded`.

| Last evidence status | Meaning | Safe action |
| --- | --- | --- |
| `unattested` | No compatible evidence has been committed | Confirm the daemon includes the attestation protocol and local reload health is advancing; restart only if the process cannot renegotiate; do not enable scans on assumption |
| `not_loaded` | The daemon explicitly reported no loaded role-source config | Use the redacted summary and local reload health to confirm the managed file exists and is accepted; wait for hot reload before considering restart |
| `config_missing` | The server source's opaque config reference is absent from the daemon's loaded set | Compare the intended desired-state document to the redacted summary; use the CAS apply flow only after human review |
| `kind_mismatch` | The same reference resolves to another adapter kind | Stop scans and correct the desired-state document; never coerce the server record |
| `adapter_version_mismatch` | The daemon loaded a different adapter contract version | Align the reviewed daemon/adapter and desired state, verify hot reload evidence, then rescan before apply |
| `invalid_attestation` | Stored or incoming evidence failed strict validation | Treat as a security/integrity incident; preserve logs, stop rollout and investigate before restarting repeatedly |
| `loaded` | The last evidence matched | Still require current runtime availability; historical success is not live health |

When a desired-state correction is approved, obtain the current revision from
`show` and use the documented compare-and-swap command. A stale revision must
fail rather than overwrite a concurrent operator's change. Key rotation is a
separate explicit operation and must not be used as a generic repair step.

## Signed-remote source operations

Treat the configured Ed25519 keys as publisher identities, not transport
credentials. Private keys must remain in the approved external signer; Multica
accepts only one to three named public keys. The source summary should show the
expected host, issuer and key-set digest without URLs or key bodies.

For routine rotation, add the next key first, publish and scan one bundle using
its new key ID, review source evidence, then remove the retired key only after
the rollback window. Never replace key material under an existing key ID. A
stale managed-config revision must fail and be re-reviewed rather than forced.

On `unknown key`, `signature verification`, `digest commitment`, TLS, redirect,
private-address, size-limit or changed-artifact failure, leave the source paused
or last-known-good active and preserve the publisher release record. Do not add
an unreviewed key, enable a proxy, permit a redirect, relax DNS/address checks or
manually edit snapshot evidence. Compare the intended release to the external
append-only release ledger; a cryptographically valid old bundle is not proof
of freshness. Follow [`signed-remote-bundle.md`](signed-remote-bundle.md) and
complete Gate R before enabling a production cohort.

## Escalation evidence

Attach the alert start/end time, deployment version, redacted source status,
redacted daemon summary, relevant bounded error codes and the read-only scan
result. Exclude paths, raw config IDs, digests, keys, environment values, MCP
payloads and artifact bodies. If PostgreSQL failover or object storage was
involved, retain the corresponding evidence required by
[`production-validation.md`](production-validation.md).

For a regional/database loss or suspected backup inconsistency, stop ordinary
repair and follow [`disaster-recovery.md`](disaster-recovery.md). A successful
`pg_restore` is not sufficient evidence; do not resume traffic until the
content-free role-source verifier passes against restored object storage and
the approved current/previous secret-key escrow.
