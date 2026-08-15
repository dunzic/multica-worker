# Role-source disaster recovery

Status: merge-ready behind disabled retention/GC gates. Production rollout
remains NO-GO until the recorded PostgreSQL 17, object-store and topology drill
in `production-validation.md` passes.

## Recovery contract

A PostgreSQL dump alone is not a role-source backup. Runnable historical state
also depends on immutable artifact bodies and, for an unexpired one-time secret
transfer, the active and decrypt-only previous master keys. Publisher private
keys and raw signed-bundle signatures never belong in Multica's backup.

`role_source_dr backup` takes the migration lock and an exclusive role-source
DR advisory lock, opens one repeatable-read transaction, exports that exact
PostgreSQL snapshot, streams every ready artifact into a deterministic archive,
and writes a content-free manifest. Historical pruning, workspace teardown and
permanent object purge take the shared side of the same lock. Normal scans and
applies continue; PostgreSQL MVCC puts their commits either before or after the
exported snapshot.

The manifest contains table row counts and SHA-256 commitments, schema migration
commitment, artifact count/bytes/commitment, archive and dump digests, and only
the key IDs needed by unexpired transfers. It contains no snapshot JSON, source
configuration, local path, request key, ciphertext, envelope, artifact body or
credential.

The immutable `role_source_artifact_purge_receipt` ledger is part of the table
inventory even though its corresponding object bodies are deliberately absent
from the artifact archive. Each row is content-free: it retains digests,
logical size, stable backend/mode, aggregate provider observations, verified
absence and completion time, never a raw storage key. Backup/restore preserves
these rows as audit evidence; it must not recreate purged bodies from receipts.

The manifest is domain-separated and Ed25519-signed. Keep the signing private
key in a KMS/HSM-backed operator secret and distribute the trusted public key
through a different configuration channel from the backup bundle. Only the
backup command has a development-only `--allow-unsigned-manifest` option;
restore and verification always reject unsigned manifests, preventing a
development artifact from becoming recovery evidence.

Bootstrap a key once, import the private file into the approved signer/KMS, then
securely delete that bootstrap file after verified import. Distribute the public
file independently:

```bash
/app/role_source_dr generate-signing-key --key-id backup-v1 \
  --private-key-file /private/bootstrap/backup-v1.private \
  --public-key-file /private/bootstrap/backup-v1.public
```

## Backup

Use PostgreSQL 17 client tools against the primary and the same object-storage
configuration as the server. The output directory must not already exist.

```bash
export MULTICA_ROLE_SOURCE_DR_SIGNING_KEY_ID='backup-v1'
export MULTICA_ROLE_SOURCE_DR_SIGNING_PRIVATE_KEY='BASE64_ED25519_PRIVATE_KEY_FROM_APPROVED_SIGNER'
/app/role_source_dr backup \
  --output-dir /private/backup/role-source-2026-08-13
```

When running inside the Helm backend pod with local PVC storage, explicitly set
`LOCAL_UPLOAD_DIR=/app/data/uploads` and mount a separate encrypted backup
volume at `/private/backup`; never write the bundle to the uploads PVC itself.
For S3, use a dedicated backup job/pod based on the same image and credentials,
with the output volume and KMS signing secret mounted only for that job. The
chart provides an explicitly default-off one-shot backup Job template; it does
not schedule backups automatically. Set a unique DNS-safe `runName`, a separate
backup PVC, a new single-directory `outputDirectory`, the signer Secret name
and optional narrowly scoped storage Secret. The Job refuses retries and the
backup tool refuses an existing directory.

The command produces `database.dump`, `artifacts.tar` and `manifest.json`, all
mode 0600 below a mode 0700 directory. Copy the bundle to approved encrypted,
immutable backup storage. Separately escrow every key ID named in
`key_requirements` through the organization's KMS/secret-backup process. Never
put key bytes beside the bundle.

## Restore drill

1. Fence the target from customer traffic, schedulers, daemons, retention and
   artifact GC. Restore `database.dump` to a fresh PostgreSQL 17 database using
   `pg_restore`; do not overwrite a running environment.
2. Point `DATABASE_URL` and the staging object-storage variables at that
   isolated target. Restore current and previous role-source secret keys from
   the approved secret escrow.
   Supply the trusted backup public key independently:

   ```bash
   export MULTICA_ROLE_SOURCE_DR_TRUSTED_PUBLIC_KEYS='{"backup-v1":"BASE64_ED25519_PUBLIC_KEY"}'
   ```
3. Rehydrate immutable objects. The command verifies archive and database
   inventory commitments before any upload, holds the exclusive DR lock so a
   mistakenly running deletion worker cannot race recovery, then uploads by
   canonical digest key and is safe to retry:

   ```bash
   /app/role_source_dr restore-artifacts \
     --manifest /private/backup/role-source-2026-08-13/manifest.json \
     --artifact-archive /private/backup/role-source-2026-08-13/artifacts.tar
   ```

4. Run the full verifier. Supplying the original files proves the copied bundle
   itself was not changed:

   ```bash
   /app/role_source_dr verify \
     --manifest /private/backup/role-source-2026-08-13/manifest.json \
     --database-dump /private/backup/role-source-2026-08-13/database.dump \
     --artifact-archive /private/backup/role-source-2026-08-13/artifacts.tar \
     --report /private/evidence/role-source-restore-report.json
   ```

5. Require `status=passed`. The verifier recomputes every table commitment;
   canonical snapshot/plan/receipt digests; complete audit chains; current,
   pin, mapping, capability, plan, apply, hold, retention and artifact edges;
   every artifact's exact byte length/SHA-256; and every unexpired transfer's
   claims, envelope digest and decryptable private key. It also recomputes every
   artifact-purge receipt commitment for both the legacy v1 contract and the
   ambiguity-aware v2 contract. It rejects unsupported versions, an invalid
   shape, changed provider mode, unverified absence, logical-byte mismatch or
   a v2 completeness flag inconsistent with its ambiguity count. A passing v2
   receipt with `provider_evidence_complete=false` still proves exact-key
   absence, but its provider-operation counts are lower bounds and must remain
   labelled that way in the restore report and any external reconciliation.
6. Start the server with retention and artifact GC still disabled. Start one
   reviewed daemon, wait for fresh loaded attestation, perform a read-only scan,
   then execute a controlled no-op plan. Re-enable traffic only after the drill
   owner signs the evidence record. Re-enable deletion workers last.

For a full Multica database recovery, preserve `channel_delivery` together with
its evidence JSON, evidence digest and `ambiguous_at`. Before starting Slack or
DingTalk outbound consumers, run the channel-delivery reconciler with provider
traffic still fenced. Restored expired `pending` rows must become
`ambiguous/lease_expired`; they must never become retryable merely because the
backup predates their send outcome. Validate every delivered, readback and
ambiguous row through the application evidence validator. If validation fails,
keep outbound connectors disabled and restore a known-good database copy rather
than editing evidence or status fields.

Expired, consumed, cancelled and failed secret transfers deliberately keep no
recoverable private key. If a historical target needs environment/MCP values,
rescan and create a fresh one-time transfer; a database restore never revives
old credentials.
