# RS-02/RS-06 versioned S3 permanent-purge review — 2026-08-15

Scope: live S3-compatible version inventory, late-write convergence, Object
Lock legal hold and IAM failure closure for role-source artifact deletion.

Decision: **GO for merge behind the existing default-off artifact-GC gate;
NO-GO for customer deletion or 10,000-user production claims until the same
suite and the receipt state machine pass on the candidate object store and
two-replica/failover topology.**

## Value and boundary

This gate closes a material gap between a mocked S3 protocol and an actual
versioned object store. It proves that the production implementation can remove
historical versions and delete markers, converge after a late authorized PUT,
and refuse to report absence when an Object Lock legal hold or IAM policy
blocks version deletion. It does not prove cloud-provider billing reduction,
multi-node durability, retention-policy correctness or recovery objectives.

## Recorded environment

- isolated Docker network with no host port and a disposable one-drive data
  volume;
- MinIO server built from official tag
  `RELEASE.2025-10-15T17-29-55Z`, commit
  `9e49d5e7a648f00e26f2246f4dc28e6b07f8c84a`, Go 1.24.8,
  linux/arm64; binary SHA-256
  `ee12f2f8ade0d0a212f021fe00bdcfa910b400db0435bdeb508a992a46e07264`;
- MinIO Client `RELEASE.2025-08-13T08-35-41Z`, commit
  `7394ce0dd2a80935aded936b09fa12cbb3cb8096`, image digest
  `sha256:a7fe349ef4bd8521fb8497f55c6042871b2ae640607cf99d9bede5e9bdf11727`;
- one ordinary versioned bucket and one bucket created with Object Lock;
- one application identity limited to bucket-version inspection, exact-prefix
  list/read/write/current-delete/version-delete, and a second identity with an
  explicit `Deny` for `s3:DeleteObjectVersion`; only a separate admin identity
  could create buckets or change legal holds.

The server process itself warned that this one-drive topology loses
availability with one host failure. This is deliberate test isolation, not a
candidate production deployment. The upstream repository is archived and its
public distribution is source-only; this exact build is therefore retained as
protocol evidence, not selected as Multica's production object-store vendor.

## Architecture expert — 2/3

- The live path exercises the same `S3Storage.PurgeObjectWithResult` used by
  role-source GC: bounded preflight inventory, current delete, fresh inventory,
  exact version/delete-marker batch deletion and final empty inventory.
- Two stored content versions and a pre-existing delete marker were removed;
  the result reported two versions, at least the prepared marker, 146 observed
  bytes and verified absence. The assertion deliberately permits provider
  differences in whether deleting a current marker creates another marker.
- A PUT after the first verified purge created one new retained version. A
  second independent purge removed that version and its marker, showing why the
  durable widening tombstone tail is required instead of claiming one request
  can prevent future writers.
- A legal-held version caused the application identity's purge to fail with
  `VerifiedAbsent=false`; the exact version remained byte-readable. Only after
  the admin identity removed the hold could the application identity complete
  the purge.
- An identity explicitly denied version deletion also failed closed and left
  the exact original version readable. The final privileged inventory found no
  validation versions or delete markers in either bucket.
- Open objections: no candidate AWS/S3-compatible service, TLS/KMS, provider
  request IDs, multi-site replication, network partition, timeout-after-delete,
  partial multi-delete transport ambiguity, live inventory above 10,000
  versions, PostgreSQL receipt integration or two-replica/primary-failover run.

## Product expert — 2/3

- The gate validates the customer promise that “verified absent” means no
  retained exact-key version, not merely an invisible current object.
- Legal retention wins over deletion and produces an operator-visible failure;
  there is no application permission to release a provider hold. This matches
  the product's separation between compliance authority and GC automation.
- Late writes converge through a later pass rather than being silently lost or
  mislabeled. Existing UI language remains correct: logical/provider-observed
  bytes are evidence, not realized or billed savings.
- Open objections: the owner/support workflow for a blocked provider hold,
  evidence export and retention terms, provider-specific error wording and
  provider-accounting reconciliation need controlled-cohort validation.

## Test expert — 2/3

- The final least-privilege run passed four opt-in tests:
  `RoundTrip`, `LateWriteConverges`, `ObjectLockFailsClosed` and
  `PermissionFailureFailsClosed`; ordinary tests skip all external I/O.
- The first live run exposed a provider-specific delete-marker count and the
  test was corrected to assert the protocol invariant instead of MinIO-specific
  marker multiplication. A second run exposed that omission alone was not a
  denial in this MinIO policy evaluation; an explicit version-delete deny then
  produced the intended failure and was retained as the auditable negative
  case.
- Default `go test ./internal/storage` passes, proving the opt-in suite compiles
  without changing ordinary CI behavior. The administrator's final recursive
  version listing was empty for both validation prefixes.
- Open objections: no live >10,000-version refusal, injected partial success in
  a real provider, concurrent PUT inside the same purge call, client/server
  process death, TLS expiry, throttling, primary failover, receipt-row
  correlation, Gate F restore or candidate-scale latency/error measurements.

## CEO / rollout owner — 2/3

- The evidence reduces deletion-audit and compliance risk for every role-source
  adapter without tying the product to AgentWaker or one object-store vendor.
- Separate application/admin identities and a destructive gate that remains
  default-off limit blast radius and make the first cohort governable.
- It does not establish 10,000-user capacity, object-store vendor support,
  incident labor, recovery cost or billed savings. Raising this score before
  candidate-topology and restore evidence would create an unsupported SLA and
  commercial claim.

## Remaining production gates

1. Re-run all four probes with the candidate least-privilege identity against
   the selected versioned object store, retaining provider request IDs and an
   independent version inventory.
2. Run the five-pass PostgreSQL receipt state machine against that same store,
   including a late PUT, legal hold, explicit IAM deny, partial multi-delete,
   timeout-after-request and process-death injection.
3. Repeat with two API/worker replicas while failing over PostgreSQL; prove one
   immutable receipt or one retryable intent, never both or neither.
4. Complete Gate F backup/restore with measured RPO/RTO and prove purged bodies
   are not recreated while receipt commitments remain valid.
5. Approve the provider retention/hold RACI, evidence retention, alert response,
   quarantine authority and independent inventory/billing reconciliation before
   any deletion or savings statement.
