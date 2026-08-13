# Daemon role-source configuration

Status: internal preview; the scan and sync feature flags remain off by default.

The daemon enables role-source scanning when a validated private config exists at the current profile's `role-sources.json`, or when `MULTICA_ROLE_SOURCE_CONFIG_FILE` explicitly selects another clean absolute path. The file must be regular, non-symlink and `0600` or stricter. An invalid configured file fails daemon loading instead of silently advertising a scanner that cannot run.

Administrators manage the file through `multica daemon role-source`. The input is a secret-free complete desired-state document; it deliberately has no `digest_key` field:

```json
{
  "version": 1,
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

Write the desired state to a private file, create with the explicit `absent` revision, and use the returned revision for every later update:

```bash
chmod 0600 /secure/path/desired-role-sources.json
multica daemon role-source apply \
  --document /secure/path/desired-role-sources.json \
  --expected-revision absent

multica daemon role-source show --output json
multica daemon role-source apply \
  --document /secure/path/desired-role-sources.json \
  --expected-revision sha256:REVISION_FROM_SHOW
```

`apply` takes a non-blocking sibling-file lock, checks the exact full-private-file revision, validates every root and adapter configuration, writes a `0600` temporary file in the same directory, syncs it, atomically replaces the target and syncs the directory. A stale revision or concurrent manager fails without publishing. The input may be `--document -` for bounded stdin; adapter JSON is never accepted on the command line.

`show` exposes only the revision, validation status/code, final file/root names, source IDs/kinds and adapter-redacted attributes. It does not expose the full path, raw adapter config or digest key. It still returns a usable revision for a malformed or currently invalid file so an administrator can recover it with `apply`.

## Live reload and recovery

The running daemon watches the resolved managed path every five seconds. A
successful `apply` or `rotate-key` is securely reread, fingerprinted, strictly
validated and built as a complete new scanner before one atomic generation
swap. New heartbeats and work use the new generation; a scan or secret transfer
already in flight retains the exact generation it started with and releases
that generation's own concurrency reservation. The daemon clears its local
attestation acknowledgements and recovery-poll throttles after a real revision
change, so every hosted runtime reports the new runtime-scoped evidence until
the server durably acknowledges it. A daemon restart is not required.

An invalid, missing, public, symlinked or unreadable replacement never unloads
an already working scanner. The daemon keeps the last-known-good generation,
logs one bounded transition warning and exposes a local-only health state:

- `loaded`: the shown revision is active;
- `unloaded`: the implicit default file has never existed, which is a valid
  disabled state;
- `degraded`: reload was rejected; `error_code` identifies only a bounded class
  such as `config_invalid`, `file_missing` or `file_security_rejected`.

`/health.role_source_config` includes status, revision and attempt/success
timestamps. It never includes the managed path, config IDs, roots, adapter
payloads or digest key. Restoring the exact last-known-good bytes clears the
degraded state without rebuilding the scanner. Creating the previously absent
default file enables the scanner live.

When the desired state first includes AgentWaker, the manager generates 32 random private bytes. Normal updates preserve that key. Removing the last AgentWaker source removes it. Rotation is a separate disruptive action:

```bash
multica daemon role-source rotate-key \
  --expected-revision sha256:REVISION_FROM_SHOW \
  --confirm-rescan
```

Rotation changes all keyed environment-value evidence. The command reports `rescan_required`; every AgentWaker source must be rescanned and reviewed before apply.

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

- the private `digest_key` is manager-owned and must never be added to the desired-state document. It exists only when at least one `agentwaker_directory` source is configured, is decoded into mutable memory, copied into that adapter and cleared after startup parsing.
- `allowed_roots` contains 1–64 existing, clean, non-root absolute directories. Symlinked roots or parent components are rejected.
- `sources` contains 1–512 opaque local config IDs. IDs may contain ASCII letters, digits, dot, underscore and dash, up to 128 characters.
- each `root_path` must be an existing non-symlink directory at or below an allowed root. Raw paths stay in this local file and never enter server persistence, member APIs, heartbeat commands, snapshots, plans, audit events or analytics.
- the daemon accepts the compile-time registered `agentwaker_directory` and `multica_manifest_directory` kinds. Both use the same queue, lease, root confinement, normalized snapshot validation, artifact reopen and content-addressed upload protocol.
- `multica_manifest_directory` publishes the normalized `Manifest` JSON contract directly. Artifact references must contain exact SHA-256, size, root-relative path and an approved text/JSON/YAML media type. This adapter deliberately has no secret-transfer authority, so configured environment values and MCP declarations are rejected rather than silently imported without values.

The server source record refers to one of these opaque IDs through `daemon_config_id` and exposes only the selected adapter's safe summary (for example, final directory and manifest file names). On startup the daemon also reports a strict, bounded attestation containing a runtime-scoped commitment to the exact private-file revision plus sorted runtime-scoped config-ID digests, kinds and adapter versions. It never reports a raw file revision, config ID, path, root, raw adapter config or key. Runtime scoping prevents local labels and a common daemon fingerprint from being correlated across workspaces hosted by one daemon. The server durably acknowledges that evidence and the daemon suppresses repeats for each runtime until the loaded state changes; the read-only settings surface shows current drift and distinct historical states without returning the digest list.

Managed local lifecycle is now available, including source addition/removal through complete desired-state apply, explicit key rotation and last-known-good hot reload. Broad customer rollout remains blocked on guided configuration/repair UI, remote or hardware-backed source trust, live database/cross-platform filesystem failure-injection evidence, and operational recovery exercises.
