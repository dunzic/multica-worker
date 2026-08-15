# RS-01/RS-02 control-plane evidence review

Features: Adapter registry/source registration and bounded scan protocol

Gate: design and merge evidence

Date: 2026-08-13

Decision: CONDITIONAL — merge behind disabled flags; no customer rollout and no daemon capability advertisement

## Customer problem and measurable outcome

An administrator can register a source without exposing its local path, queue a read-only scan, and observe scan status. The server can safely negotiate one leased scan with a capable daemon without turning every 15-second fleet heartbeat into a PostgreSQL poll. No scan path is available to ordinary agents, and no source registration or scan endpoint is discoverable while the flags are off.

## Architecture expert review

Score: 2/3

Accepted evidence:

- the server uses a descriptor-only catalog and cannot instantiate the filesystem-capable AgentWaker scanner;
- source registration stores an opaque daemon config handle plus a bounded typed summary, never raw adapter config;
- workspace member reads and admin mutations are enforced by route middleware; agent actors cannot register or request scans;
- `role_source_sync` and `role_source_scan` are both required and default closed; apply has a separate flag;
- heartbeat support negotiation and polling are separate, so capability-only heartbeats perform no scan-queue database claim;
- a queued scan sends a best-effort pending-work hint; an explicit daemon poll leases at most one request for two minutes;
- daemon commands contain no source path, credential, manifest or artifact body;
- terminal reports bind workspace, source, request, runtime and lease; exact duplicate success/failure reports are idempotent, while conflicting/stale reports fail closed.

Open objections:

- the daemon-local encrypted/permissioned configuration store and scan executor are not implemented, so current daemon binaries intentionally do not advertise scan support;
- workspace deletion does not yet explicitly remove all role-source records;
- no live PostgreSQL contention/lease-expiry result or cluster failover result exists;
- the server currently trusts the admin-supplied safe config summary; the completed local configuration workflow must derive and sign or echo it from the daemon.

## Product expert review

Score: 2/3

Accepted evidence:

- the API exposes adapter capabilities, safe source state and scan progress without local-machine details;
- duplicate active scans produce a clear conflict and failed scans preserve the active worker;
- scan scheduling and apply can be rolled out or killed independently.

Open objections:

- no source setup, diagnostics, progress, detach/pause or preview UI exists;
- daemon-offline and local-config-missing recovery language has not been designed;
- safe summary labels are adapter-defined and still need localized product copy.

## Test expert review

Score: 2/3

Accepted evidence:

- tests prove default-off endpoints do not call the control plane, unknown JSON fields fail closed, agent-facing responses omit daemon config and lease tokens, active-scan conflicts map correctly, and the owning runtime is nudged;
- protocol tests prove capability negotiation performs no database claim and a poll produces the bounded leased command;
- targeted ordinary and race-enabled handler/registry/AgentWaker tests pass; vet passes for all touched server/daemon packages;
- daemon and WebSocket packages compile with the new backward-compatible JSON fields.

Open objections:

- listener-based daemon/WebSocket tests cannot run in the current sandbox and must pass in CI;
- exact duplicate terminal-report behavior needs live transaction integration tests;
- no malformed 16 MiB report, cross-workspace daemon token, expired lease, concurrent claim or database restart test exists;
- live migration round-trip remains skipped because local PostgreSQL is unavailable.

## CEO review

Score: 2/3

Accepted evidence:

- descriptor/control-plane separation preserves a generic ecosystem boundary rather than embedding AgentWaker in product APIs;
- preview-only scan can be released independently from the materially riskier apply and secret features;
- explicit polling avoids a fleet-wide empty-query tax, supporting the 10,000-user cost target by design.

Open objections:

- no end-to-end customer value is available until daemon execution and preview UI exist;
- scan cost, queue latency, operator load and support cost are unmeasured;
- a second adapter and customer design partners are still required to validate ecosystem leverage.

## Security, privacy and data-loss blockers

- daemon support must remain unadvertised until local config permissions, root allowlisting, digest-key storage and terminal-report retry are implemented and tested;
- apply and secret-transfer flags remain off;
- workspace teardown and retention/GC must be complete before any cohort rollout;
- no rollout is permitted without cross-tenant authorization and live lease-concurrency evidence.

## Actions

| Action | Owner | Gate |
| --- | --- | --- |
| Implement daemon-local config store, AgentWaker registry and bounded executor | implementation + security | RS-02 evidence |
| Add hint-driven immediate poll and throttled recovery poll; only then advertise capability | implementation | RS-02 evidence |
| Add explicit workspace teardown in dependency order | implementation + test | RS-01 evidence |
| Run CI listener tests and live PostgreSQL lease/retry/failover tests | test | pre-merge/rollout |
| Build source setup/status/diagnostic UI and localization | product | RS-01/RS-02 rollout |
| Execute target-profile load test and publish cost/latency evidence | architecture + CEO | rollout |

Final decision: CONDITIONAL
