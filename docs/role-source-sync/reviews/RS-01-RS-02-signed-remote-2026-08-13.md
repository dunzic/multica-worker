# RS-01/RS-02 signed-remote adapter review

Date: 2026-08-13

Final decision: **GO for merge behind existing sync/scan flags; NO-GO for a
production remote-source cohort until Gate R and publisher anti-replay policy
are recorded**

## Architecture expert — 2/3

- `multica_signed_remote` is a third compile-time adapter over the same
  descriptor, registry, snapshot and artifact interfaces; remote code is never
  loaded or executed.
- A fixed Ed25519 commitment binds domain, schema version, issuer, key ID,
  revision, canonical manifest digest and canonical artifact-tree digest.
  Snapshot evidence adds the selected key ID without changing old empty-field
  snapshot encoding.
- Credential-free same-origin HTTPS, no redirects/proxy/IP literal/non-default
  port, DNS-to-public-address-only dialing, TLS/time/connection/size bounds and
  per-artifact digest verification constrain the remote authority.
- One-to-three named public keys support staged rotation; daemon-config hot
  reload and runtime attestation show when the trust set is loaded.
- Open objections: no transparency log or monotonic revision authority, live
  DNS/TLS/CDN behavior is unmeasured, and binary/private-registry authentication
  is intentionally unsupported.

## Product expert — 2/3

- Teams can consume an organisation-managed role catalog without copying it
  onto every daemon host, while preserving the existing review/apply workflow.
- Safe summary exposes host, issuer and key-set digest; URLs and key bodies stay
  out of the control plane and settings surfaces.
- Rotation has an overlap workflow instead of a coordinated hard cutover. An
  outage or invalid release leaves last-known-good materialization active.
- Open objections: there is no guided setup/rotation wizard, signer tooling or
  freshness warning in the UI; signed origin does not mean trusted role intent.

## Test expert — 2/3

- Unit and composition tests cover strict config/envelope decoding, canonical
  digest/signature stability, tamper/wrong/unknown/retired key, staged rotation,
  changed body, redaction, cancellation, unsupported authority and rootless
  daemon loading through the shared registry.
- Public/private IPv4/IPv6 classification, same-origin/port/credential/query
  constraints and bounded client construction are deterministic tests.
- Open objections: no live DNS rebinding, TLS expiry, redirect/CDN, slow-body,
  hardware signer, multi-platform or 100-concurrent-scan evidence exists.

## CEO / rollout owner — 2/3

- This materially expands Multica from host-local import to centrally published
  role catalogs without creating a separate product or provider-specific data
  model.
- The trust boundary is commercially explainable: the signer proves publisher
  identity, while Multica approval proves deployment authorization.
- Existing default-off flags and last-known-good behavior contain early pilots.
- Open objections: approve publisher ownership/SLA, key-compromise RACI,
  transparency/rollback policy and support telemetry before external claims.

## Required next evidence

Run Gate R against the actual DNS, TLS, CDN/object store and signer path; record
all four review scores at 3/3; then exercise key compromise and old-release
replay during the disaster-recovery drill. Unit verification is not proof of
remote freshness, availability or production key custody.
