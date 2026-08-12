# Role-source runtime and attestation recovery runbook

This runbook restores the control plane without modifying role-source tables by
hand. Use an administrator account for the affected workspace. Never paste the
daemon's private config file, digest key, environment values or MCP credentials
into tickets, chat or logs.

## Alert: `MulticaRoleSourceRuntimeUnavailable`

The alert means at least one `registered`, `active` or `error` role source has
remained bound to a missing, offline or database-stale runtime for ten minutes.
The metric is deliberately fleet-level and contains no workspace, source or
runtime identifiers.

1. Check `MulticaBusinessSamplerQueryErrors` first. If query
   `role_source_runtime_availability` is failing, treat the availability gauge
   as stale and repair the metrics read pool or PostgreSQL before diagnosing a
   daemon.
2. In the authenticated workspace settings, open **Role sources** and filter
   for **Runtime offline or stale**. Record the workspace, source name, source
   state, runtime status, last evidence status and last observation time in the
   incident record. Do not copy opaque digests unless the incident store is
   approved for audit identifiers.
3. Confirm whether the bound daemon process is running and can reach the same
   Multica server and PostgreSQL-backed control plane. Check server and daemon
   logs for heartbeat authentication, websocket reconnect or attestation
   persistence errors. Do not infer health from a running process alone.
4. On the daemon host, as the same operating-system account that owns the
   daemon, inspect only the redacted local summary:

   ```bash
   multica daemon role-source show --output json
   ```

   Confirm that the expected config entry exists and that its adapter kind and
   version match the server source. Do not read or copy the private managed file
   directly.
5. If the configuration is correct, restart the daemon through that
   installation's service manager. Within three minutes the runtime should be
   online; on a fresh process start it must also resend loaded-configuration
   evidence until the server durably acknowledges it.
6. Verify all of the following before resolving the incident:

   - the source's effective runtime-config status is `loaded`;
   - its preserved attestation status is `loaded`;
   - its runtime status is `online`;
   - the last observation advances after the restart;
   - `multica_role_source_runtime_availability{status="runtime_unavailable"}`
     falls by the expected count and the alert clears after its hold period;
   - a read-only scan completes before any apply operation is considered.

If the daemon is intentionally retired, do not update PostgreSQL manually and
do not repoint the source by editing opaque IDs. The current product does not
yet expose a controlled pause/detach/rebind workflow; leave apply disabled and
escalate to the role-source control-plane owner. This is an explicit production
rollout blocker, not an operator workaround.

## Attestation status recovery

The effective status and last evidence status answer different questions. A
runtime can be unavailable while its last valid evidence remains `loaded`.

| Last evidence status | Meaning | Safe action |
| --- | --- | --- |
| `unattested` | No compatible evidence has been committed | Confirm the daemon includes the attestation protocol, then restart it; do not enable scans on assumption |
| `not_loaded` | The daemon explicitly reported no loaded role-source config | Use the redacted local summary to confirm the managed file exists and is accepted, then restart |
| `config_missing` | The server source's opaque config reference is absent from the daemon's loaded set | Compare the intended desired-state document to the redacted summary; use the CAS apply flow only after human review |
| `kind_mismatch` | The same reference resolves to another adapter kind | Stop scans and correct the desired-state document; never coerce the server record |
| `adapter_version_mismatch` | The daemon loaded a different adapter contract version | Align reviewed daemon and source versions, restart and rescan before apply |
| `invalid_attestation` | Stored or incoming evidence failed strict validation | Treat as a security/integrity incident; preserve logs, stop rollout and investigate before restarting repeatedly |
| `loaded` | The last evidence matched | Still require current runtime availability; historical success is not live health |

When a desired-state correction is approved, obtain the current revision from
`show` and use the documented compare-and-swap command. A stale revision must
fail rather than overwrite a concurrent operator's change. Key rotation is a
separate explicit operation and must not be used as a generic repair step.

## Escalation evidence

Attach the alert start/end time, deployment version, redacted source status,
redacted daemon summary, relevant bounded error codes and the read-only scan
result. Exclude paths, raw config IDs, digests, keys, environment values, MCP
payloads and artifact bodies. If PostgreSQL failover or object storage was
involved, retain the corresponding evidence required by
[`production-validation.md`](production-validation.md).
