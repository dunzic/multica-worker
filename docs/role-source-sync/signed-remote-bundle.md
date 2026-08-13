# Signed remote role-source bundle

Status: implemented behind the existing role-source sync/scan flags; production
enablement still requires the remote-origin validation gate below.

Adapter kind: `multica_signed_remote`

Adapter version: `0.1.0`

Signature: Ed25519 over a domain-separated canonical commitment

## Trust and network contract

The daemon accepts a credential-free canonical HTTPS bundle URL, a same-origin
artifact base URL, an issuer, and one to three named Ed25519 public keys. It
does not accept URL credentials, query strings, fragments, IP literals,
non-default ports, redirects, environment proxies, or a different artifact
origin. DNS results are resolved by the daemon and only public unicast
addresses are dialled; loopback, private, link-local, multicast and unspecified
addresses fail closed. Requests have connection, header and total deadlines,
small connection pools, disabled compression, and 8 MiB response limits.

This is publisher authenticity, not publisher safety. A valid signer can still
publish destructive role intent. The normal snapshot, plan, approval, apply,
permission and legal-retention gates remain mandatory.

Managed daemon configuration is secret-free:

```json
{
  "kind": "multica_signed_remote",
  "config": {
    "bundle_url": "https://publisher.example/roles/bundle.json",
    "artifact_base_url": "https://publisher.example/roles/artifacts/",
    "issuer": "publisher.example",
    "public_keys": {
      "2026-primary": "BASE64_ED25519_PUBLIC_KEY",
      "2026-next": "BASE64_ED25519_PUBLIC_KEY"
    }
  }
}
```

The redacted summary exposes only host, issuer and one digest of the full key
set. It never returns URLs, paths or public-key bodies. A remote-only managed
document may use an empty `allowed_roots` array; adding a filesystem adapter
still requires at least one canonical allowed root.

## Bundle envelope

The HTTPS endpoint returns strict JSON with no unknown or trailing fields:

```json
{
  "version": 1,
  "issuer": "publisher.example",
  "key_id": "2026-primary",
  "revision": "release-2026.08.13",
  "manifest_digest": "sha256:...",
  "tree_digest": "sha256:...",
  "manifest": { "contract_version": "1.0", "roles": [], "capabilities": [] },
  "signature": "BASE64_ED25519_SIGNATURE"
}
```

`manifest_digest` is SHA-256 over the JSON encoding of the validated,
source-neutral manifest after Multica canonical ordering. `tree_digest` is
SHA-256 over a JSON array of every artifact commitment
`{path,digest,size_bytes,media_type}`, sorted by those four fields. Repeated
references remain repeated commitments. The Ed25519 message is this fixed-field
JSON object in Go struct field order:

```json
{"domain":"multica-role-source-signed-bundle-v1","version":1,"issuer":"publisher.example","key_id":"2026-primary","revision":"release-2026.08.13","manifest_digest":"sha256:...","tree_digest":"sha256:..."}
```

The adapter recomputes both digests, requires the envelope key ID to exist in
the configured trust set, and verifies the signature before it returns a
snapshot. Source evidence records issuer, key ID, revision, tree digest and a
SHA-256 commitment to the signature—not the signature or key itself. Each
artifact is fetched from the escaped relative path under `artifact_base_url`,
held within the 8 MiB bound, and rechecked against exact size and SHA-256 before
the existing daemon-authenticated upload path can publish it.

Configured environment values and MCP definitions are rejected because this
adapter advertises no secret-transfer authority. The first version also allows
only text, Markdown, JSON and YAML artifacts; it does not advertise binary
artifact support.

## Staged key rotation

1. Add the next public key under a new key ID while the publisher still signs
   with the current key. Apply the managed daemon document through normal CAS
   and wait for loaded-config evidence.
2. Publish a bundle signed by the next key ID. Scan and review its source
   evidence before any apply.
3. After the rollback window and all relevant daemons have loaded the new trust
   set, remove the retired key with another CAS update.

Never reuse a key ID for different key material. Keep private keys outside
Multica and the daemon configuration, preferably in a hardware-backed signer.
If a key is compromised, pause affected sources, preserve bundle and audit
evidence, remove the key from every daemon trust set, publish a clean revision,
then rescan and review before resume.

## Availability and replay behavior

An origin outage, DNS failure, redirect, invalid certificate, oversized body,
unknown key, bad signature or changed artifact fails the scan and leaves the
last applied snapshot active. Replaying an old but still valid signed bundle can
produce its old immutable snapshot; Multica does not infer freshness from a
revision label. Production publishers therefore need an append-only release
ledger or transparency service, origin object versioning and an operator policy
that rejects revision rollback unless it is an approved forward rollback.

Before customer enablement, execute the signed-remote gate in
[`production-validation.md`](production-validation.md) with the actual DNS,
TLS, CDN/object storage and key-management path.
