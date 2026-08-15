# RS-06 AWS KMS manifest signing review — 2026-08-15

Feature: disaster-recovery manifest key custody and long-retention rotation

Gate: code/Helm contract and simulated provider fault matrix
Decision: **GO to merge with the backup Job default-off; NO-GO for production
Gate F until the same contract passes against the candidate KMS/HSM, workload
identity and retained audit export.**

## Four-perspective strategy meeting

| Perspective | Score | Decision and remaining objection |
| --- | ---: | --- |
| Architecture expert | 2/3 | New manifests sign a domain-separated SHA-512 commitment under a versioned scheme small enough for KMS RAW signing, while manifests without a scheme retain exact legacy-v1 verification. AWS signing requires Ed25519/SIGN_VERIFY/ED25519_SHA_512 metadata, an independently pinned public key, a resolved immutable ARN and local response verification. Helm KMS mode injects no signing private key. A 3 still requires real KMS protection/IAM/audit evidence, network fault behavior and candidate-topology restore. |
| Product expert | 2/3 | Operators receive explicit private-key compatibility and production KMS paths, stable fail-closed messages, a rotation sequence and recovery trust overlap. There is no scheduled-drill UX, trust-set inventory UI, expiry warning or guided incident workflow; those remain controlled operational procedures. |
| Test expert | 2/3 | Unit faults cover wrong spec/usage/algorithm/DER/pin, disabled/revoked and unavailable service errors, response key/algorithm mismatch, signature tamper, raw-key ambiguity, unknown provider and error redaction. Protocol tests cover legacy verification, downgrade/substitution refusal and the sub-4-KiB message. Helm tests prove default-off behavior, workload-identity requirement and absence of a private-key variable in KMS mode. No real `Sign`/`GetPublicKey`, CloudTrail, IAM deny, throttling or live rotation has run. |
| CEO | 2/3 | Removing private-key delivery from the production backup Job materially reduces an enterprise security objection and preserves historical restore value through 128 bounded trust generations. SLA, KMS cost, recovery labor and incident ownership are still unmeasured, so this cannot justify a 10,000-user launch claim. |

No perspective can score 3 from a fake client or rendered manifest.

## Executed local evidence

- focused ordinary tests, race tests, targeted `go vet` and DR command build
  passed;
- full non-root Go 1.26 `go test -count=1 ./...`, `go vet ./...` and
  `go build ./...` passed with the repository mounted read-only;
- Helm lint and real template rendering passed private-key compatibility, KMS
  workload identity, immutable ARN/pin values, S3-only TLS endpoint rendering,
  default-off and negative cases; the KMS Job contained no
  `MULTICA_ROLE_SOURCE_DR_SIGNING_PRIVATE_KEY` or global AWS endpoint override;
- ShellCheck passed the Helm and RS-06 scripts;
- three fresh packaged v2 runs killed restore at 393,216, 1,998,848 and 425,984
  partial bytes; a final scheme-assertion smoke killed at 3,342,336 bytes. All
  four runs restored 25 tables and two artifacts totaling 67,108,902 bytes,
  preserved atomic visibility, resumed/idempotently restored, retained
  `INCOMPLETE` on failed dump and rejected archive, missing/changed object and
  changed-database faults. The three formal bundles were
  67,548,412–67,548,434 bytes and completed in 12–13 seconds.

These packaged runs use the explicit local `private_key` compatibility provider
to exercise v2 serialization and verification. They are not evidence that an
AWS KMS request, IAM policy or provider audit event worked.

## Accepted design

- `signature_scheme=ed25519-sha512-commitment-v2` is signed as part of the
  canonical manifest. Removing it routes verification to the legacy domain and
  therefore invalidates the signature; unknown schemes fail before trust lookup.
- The KMS message is a signature domain plus a 64-byte SHA-512 commitment of a
  separate commitment domain and canonical manifest. The complete message is
  bounded independently of manifest size and is sent with
  `ED25519_SHA_512`/`RAW`.
- `GetPublicKey` must return `ECC_NIST_EDWARDS25519`, `SIGN_VERIFY`, the required
  signing algorithm and a DER public key exactly equal to the operator pin.
- The returned immutable key ARN, rather than a mutable alias, is used for
  `Sign`. Response key ID/algorithm must match and the signature is verified
  locally before the manifest is mutated or written.
- AWS provider details and key identifiers from service failures are not
  propagated. KMS operations inherit the backup cancellation context and have
  a 15-second application deadline.
- KMS mode rejects global and service-specific AWS endpoint variables before
  output or storage access, disables shared AWS configuration files, and
  reinstalls the official region/FIPS-aware KMS resolver. A custom object store
  receives only `S3_ENDPOINT_URL`, so it cannot redirect STS or KMS traffic.
- KMS mode rejects raw signing-private-key configuration and has no unsigned or
  private-key fallback. Helm additionally requires a dedicated service account
  and immutable key ARN and renders no signing-private-key environment entry.
- Static AWS access/session credentials, profiles and shared credential files
  are rejected in KMS mode. Helm rejects a storage Secret in this mode, so S3
  and KMS use the reviewed workload identity rather than an env credential that
  would take precedence in the default chain. Resolved credentials must report
  the SDK's Web Identity or container-endpoint source; shared/static/profile,
  assume-role and node/EC2 sources fail closed.
- Recovery trust remains independent of the signing path. Its key-generation
  bound grows from 8 to 128 under the existing 64 KiB input bound so a realistic
  rotation/retention window does not silently orphan old backups.

## Threat and fault disposition

| Threat/fault | Expected result | Current evidence |
| --- | --- | --- |
| Manifest larger than KMS RAW limit | compact signing message remains below 4 KiB | deterministic protocol unit test |
| Signature-scheme removal/substitution | verification fails | legacy/downgrade tests |
| Alias rotates between metadata and sign | resolved ARN is signed; pin and response ID remain exact | fake request assertions |
| Wrong key spec, usage, algorithm or public key | backup fails before `Sign` | metadata matrix |
| KMS disabled, permission revoked or unavailable | backup fails; no fallback | provider-error matrix |
| KMS returns corrupted signature or inconsistent metadata | local verification fails; manifest is not published | response-tamper matrix |
| S3-compatible endpoint override captures STS/KMS traffic | global/service endpoint variables fail before output/storage; shared files are disabled; official KMS resolver remains forced | preflight, SDK-option and Helm render tests |
| Raw private key remains in KMS Job | Helm render and runtime configuration fail | Helm and provider tests |
| Static S3 or node credentials shadow workload identity for KMS | KMS mode rejects static AWS env/profile, Helm storage Secret and non-workload resolved sources | configuration/source and negative render tests |
| Rotation exceeds eight retained backups | up to 128 public trust generations remain bounded | 64-key acceptance / 129-key refusal |
| Cloud credentials or signing audit are mis-scoped | production remains NO-GO | candidate Gate F required |

## Production evidence still required

1. Provision the candidate KMS key with the approved protection level and a
   dedicated workload identity limited to `GetPublicKey` and `Sign` on one key.
2. Run one successful backup/restore, then inject IAM deny, key disable,
   throttling, network loss and a mutable-alias mismatch. Retain redacted Job
   logs and provider audit events.
3. Rotate A to B with both public keys trusted, restore one backup from each,
   revoke A signing permission and prove both retained backups still verify.
4. Run the full Gate F database/object-store fault matrix under concurrent
   scan/apply/deletion traffic and record RPO/RTO, KMS latency/error rate and
   operator actions.
5. Obtain named SRE, security, product and executive approval. Any missing
   provider audit event, private-key exposure, silent fallback or unverifiable
   retained backup is an immediate NO-GO.
