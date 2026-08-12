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
    }
  }
}
```

Rules:

- `digest_key` is local HMAC material for non-reversible environment-value change evidence. It is decoded into mutable memory, copied into the adapter and cleared after startup parsing. Rotating it changes sensitive-value evidence and therefore requires an explicit rescan/review workflow.
- `allowed_roots` contains 1–64 existing, clean, non-root absolute directories. Symlinked roots or parent components are rejected.
- `sources` contains 1–512 opaque local config IDs. IDs may contain ASCII letters, digits, dot, underscore and dash, up to 128 characters.
- each `root_path` must be an existing non-symlink directory at or below an allowed root. Raw paths stay in this local file and never enter server persistence, member APIs, heartbeat commands, snapshots, plans, audit events or analytics.
- this first daemon version accepts only `agentwaker_directory`; adding another adapter requires compile-time registration and the common conformance/review gates.

The server source record refers to `agentwaker-main` through `daemon_config_id` and exposes only the adapter-produced safe summary (for example, the final directory name). The current file workflow is suitable for controlled engineering validation, not broad customer rollout: managed create/rotate/remove commands and config-summary attestation are still required.
