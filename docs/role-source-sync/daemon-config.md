# Daemon role-source configuration

Status: internal preview; the scan and sync feature flags remain off by default.

The daemon enables role-source scanning only when `MULTICA_ROLE_SOURCE_CONFIG_FILE` points to a clean absolute path containing a regular, non-symlink file with permissions `0600` or stricter. An invalid configured file fails daemon configuration loading instead of silently advertising a scanner that cannot run.

```json
{
  "version": 1,
  "digest_key": "BASE64_OF_EXACTLY_32_RANDOM_BYTES",
  "allowed_roots": [
    "/srv/multica-role-sources"
  ],
  "sources": {
    "agentwaker-main": {
      "kind": "agentwaker_directory",
      "config": {
        "root_path": "/srv/multica-role-sources/agentwaker"
      }
    },
    "standard-catalog": {
      "kind": "multica_manifest_directory",
      "config": {
        "root_path": "/srv/multica-role-sources/standard-catalog",
        "manifest_path": "multica-role-source.json"
      }
    }
  }
}
```

Minimal `multica-role-source.json` shape (the referenced file must exist beside it and match the declared digest and size):

```json
{
  "contract_version": "1.0",
  "roles": [
    {
      "id": "writer",
      "display_name": "Writer",
      "version": "1.0.0",
      "lifecycle": "active",
      "instructions": {
        "digest": "sha256:REPLACE_WITH_64_LOWERCASE_HEX",
        "path": "roles/writer.md",
        "media_type": "text/markdown",
        "size_bytes": 123
      },
      "skills": [],
      "capability_bindings": [],
      "environment": [],
      "mcp": [],
      "automations": []
    }
  ],
  "capabilities": []
}
```

Rules:

- `digest_key` is required only when at least one `agentwaker_directory` source is configured. It is local HMAC material for non-reversible environment-value change evidence, decoded into mutable memory, copied into that adapter and cleared after startup parsing. Rotating it changes sensitive-value evidence and therefore requires an explicit rescan/review workflow. A manifest-directory-only daemon may omit it.
- `allowed_roots` contains 1–64 existing, clean, non-root absolute directories. Symlinked roots or parent components are rejected.
- `sources` contains 1–512 opaque local config IDs. IDs may contain ASCII letters, digits, dot, underscore and dash, up to 128 characters.
- each `root_path` must be an existing non-symlink directory at or below an allowed root. Raw paths stay in this local file and never enter server persistence, member APIs, heartbeat commands, snapshots, plans, audit events or analytics.
- the daemon accepts the compile-time registered `agentwaker_directory` and `multica_manifest_directory` kinds. Both use the same queue, lease, root confinement, normalized snapshot validation, artifact reopen and content-addressed upload protocol.
- `multica_manifest_directory` publishes the normalized `Manifest` JSON contract directly. Artifact references must contain exact SHA-256, size, root-relative path and an approved text/JSON/YAML media type. This adapter deliberately has no secret-transfer authority, so configured environment values and MCP declarations are rejected rather than silently imported without values.

The server source record refers to one of these opaque IDs through `daemon_config_id` and exposes only the selected adapter's safe summary (for example, final directory and manifest file names). The current file workflow is suitable for controlled engineering validation, not broad customer rollout: managed create/rotate/remove commands and config-summary attestation are still required.
