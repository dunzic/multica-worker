# RS-05 redacted configuration-change review

Date: 2026-08-13

## Outcome

Workspace members can inspect environment-key and MCP-server changes for one
exact persisted role-source plan before approval or apply. The endpoint
revalidates the stored deterministic plan and projects only object category,
role ID, key/server ID and create, update, archive-candidate or blocked status.
The first page is bounded to 100 changes and includes content-free environment
and MCP counts.

The review deliberately derives from plan actions instead of loading mutable
source data or returning snapshot objects. Environment values, keyed value
digests, MCP definition hashes, referenced environment lists, URLs, commands,
arguments and headers are structurally absent from the response schema. The
shared Web/Desktop settings view states this boundary and exposes no reveal or
copy-definition control.

## Authorization and lifecycle

- Read access matches the existing member-visible immutable plan evidence.
- Creating transfers still requires an exact approved plan and owner/admin
  authority; viewing the summary never authorizes secret movement.
- The response is plan-digest bound. The Core client fails closed if the server
  returns another plan identity or any undeclared field.
- Apply continues to verify the exact one-time transfer set and revalidate
  HMAC/definition commitments against the approved snapshot.

## Verification

- Go tests construct real before/after snapshots with changed secret HMAC and
  MCP definition commitments, build the deterministic plan and assert that the
  review reports both changes without serializing either commitment.
- Handler tests assert the closed response surface and forbidden-field absence.
- Core API tests accept the safe projection and reject an injected value digest.
- Shared Views tests render key/service IDs and confirm sensitive definition
  markers do not appear.
- Go focused tests and Core/Views typechecks and full test workspaces pass.

## Remaining evidence

This is identifier-level review, not a plaintext or field-level diff. Live
PostgreSQL lifecycle, expiry, failover, key rotation and exfiltration exercises
remain required before enabling secret transfer broadly.
