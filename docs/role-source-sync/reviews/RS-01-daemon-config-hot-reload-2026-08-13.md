# RS-01 daemon role-source configuration hot-reload review

Date: 2026-08-13

Final decision: **GO for merge behind `role_source_sync` and
`role_source_scan`; NO-GO for broad production until live Windows/service,
network-filesystem and end-to-end server acknowledgement fault exercises pass**

## Architecture expert

- The daemon securely rereads the regular private file every five seconds and
  fingerprints it before strict decode and adapter construction. An unchanged
  validated revision avoids rebuilding up to 512 configurations.
- A new scanner is fully built before one atomic pointer publication. Every
  heartbeat captures the exact generation it advertised; scans, secret export,
  artifact reopen and semaphore release remain on that generation even if a
  new one is published concurrently.
- Invalid JSON, permission widening, symlink substitution, deletion and read
  failure cannot clear a working scanner. Last-known-good authority remains in
  memory and the daemon moves to a bounded degraded health state.
- A real revision change clears only local attestation acknowledgements and
  recovery-poll throttles. Server durability and acknowledgement remain the
  authority for suppressing repeated evidence.

Open objections: polling still reads and hashes up to 1 MiB every five seconds;
this is bounded and host-local but needs fleet CPU/I/O observation. Polling and
atomic replacement need live Windows service-account, NFS/SMB and power-loss
evidence. Attestation remains daemon self-evidence, not hardware-rooted proof.

## Product expert

- Operators can apply, add/remove sources and rotate a key without restarting
  the daemon or interrupting unrelated agent tasks.
- `/health.role_source_config` distinguishes valid disabled (`unloaded`),
  active (`loaded`) and last-known-good fallback (`degraded`) states with
  bounded recovery codes and timestamps.
- The health contract omits paths, local config IDs, roots, adapter payloads
  and keys. The redacted `show` command remains the repair surface.
- Restoring the exact good bytes recovers live; creating a previously absent
  default file enables scanning without a restart.

Open objections: no guided configuration/repair UI consumes this health state,
and there is no explicit user notification when a daemon remains degraded.

## Test expert

- Unit tests cover valid swap, new attestation negotiation, recovery-poll reset,
  malformed replacement, deleted file, same-revision recovery and absent-file
  enablement.
- Generation tests prove a reservation acquired from the old scanner is not
  released into the new scanner after publication.
- Health serialization tests reject leakage of the managed path, root, raw
  config ID and key material.
- Concurrent heartbeat, acknowledgement, health and repeated atomic config
  replacement pass under the Go race detector.

Open objections: the tests do not yet kill the daemon during manager publish,
exercise real Windows replacement/ACL behavior, run over NFS/SMB, or prove the
full daemon-to-two-server acknowledgement/history sequence under failover.

## CEO / rollout owner

- Value: a 10,000-user deployment no longer needs coordinated daemon restarts
  for routine role-source configuration, reducing downtime and support burden.
- Risk containment: bad automation or partial operator recovery cannot silently
  remove already working local authority; new work moves only to a completely
  validated generation and old work completes consistently.
- Cost: steady state is one bounded local read/hash per configured daemon every
  five seconds, with no server request and no new tenant-labelled metric series.

Decision: merge behind the existing disabled role-source rollout and permit an
engineering cohort after the production-validation hot-reload exercise is
recorded. Keep broad rollout and `role_source_apply` disabled until the open
cross-platform, filesystem, failover and guided-repair gates close.
