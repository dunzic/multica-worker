# RS-06 snapshot version comparison review

Date: 2026-08-13

## Outcome

Authenticated workspace members can compare two immutable role-source
snapshots without downloading either normalized manifest. The browser receives
at most 50 verified snapshot summaries and one page of at most 100 object-level
changes. A change contains only stable object kind, source ID, optional parent
ID, display name and `added`, `changed` or `removed` status.

The server loads both tenant/source-scoped snapshots, recomputes and validates
their persisted snapshot and manifest identities, compares roles, skills and
capabilities by stable ID, sorts the result deterministically and only then
returns the selected page. Role comparison excludes its skill collection so a
skill-only edit is not also reported as an opaque whole-role change.

## Safety and product boundary

- Manifest bodies, instructions, skill content, artifact paths and digests,
  environment keys, MCP definitions and automation prompts are structurally
  absent from the comparison response.
- The endpoint rejects equal, malformed or cross-source snapshot identities and
  invalid pagination before returning data.
- The response is an inspection aid, not an approval. Applying or rolling back
  still requires a deterministic plan, impact review, explicit decisions,
  approval and the atomic apply gate.
- The existing full-snapshot evidence endpoint remains unchanged for API
  compatibility; the shared operator UI only calls the bounded summary and
  comparison endpoints.

## Verification

- Go unit tests cover deterministic object ordering, role/skill separation and
  the content-free response shape.
- Handler tests cover bounded pagination, query propagation, invalid-page 400s
  and forbidden-field absence.
- Core API tests fail closed on malformed summaries and comparison identity
  mismatch.
- Shared Views tests cover the version selector, change list, boundary notice
  and pagination controls in the common Web/Desktop surface.
- Go race tests, Core/Views typechecks and the full Core/Views test workspaces
  pass in the local environment.

## Remaining evidence

Live PostgreSQL query/memory measurements with two maximum-size snapshots are
still required before setting a production SLO. The current implementation
prevents browser/network amplification but intentionally validates both full
snapshot bodies on the server. Search, export and richer field-level diffs are
not implemented; any future version must preserve the closed response schema or
introduce a separately reviewed permission boundary.
