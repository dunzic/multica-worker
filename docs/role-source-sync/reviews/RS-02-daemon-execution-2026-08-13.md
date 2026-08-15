# RS-02 daemon execution evidence review

Feature: Bounded scan and contract validation

Gate: daemon composition design and merge evidence

Date: 2026-08-13

Decision: CONDITIONAL — configured engineering cohorts may exercise scans behind disabled-by-default server flags; no production rollout

## Architecture expert review

Score: 2/3

Accepted evidence:

- only the daemon loads raw source config and constructs the filesystem adapter; the server retains a descriptor-only catalog;
- local config is size-bounded, strict-schema, non-symlink, `0600`, identity-rechecked after open, and holds an exactly 32-byte decoded digest key;
- allowed roots reject filesystem root, symlink components, non-directories and out-of-policy source roots;
- the bounded filesystem rechecks root identity after secure open and confines every descendant operation;
- capability advertisement occurs only when valid local config loaded; polling is separated and throttled to avoid fleet heartbeat database load;
- two scan slots bound local CPU/I/O; a completion triggers one drain poll rather than unbounded polling;
- 15-minute leases renew with workspace/source/request/runtime/token binding; scan cancellation reserves terminal-report time when renewal is exhausted;
- workspace shared mutation locks and explicit bottom-up teardown prevent no-FK orphan races;
- exact terminal retries retain and revalidate the private lease token but member APIs never serialize it.

Open objections:

- local config has a manual file lifecycle and no OS-keychain/ACL-specific implementation for Windows;
- root/config identity protection covers common replacement races but still needs adversarial filesystem and network-mount testing;
- artifact bodies are not transferred, so snapshots cannot yet support materialization;
- scan errors intentionally collapse to a small safe taxonomy and need adapter-specific actionable diagnostics without path leakage.

## Product expert review

Score: 1/3

Accepted evidence:

- an operator can explicitly allow only selected directories and map stable local IDs to server sources;
- offline/missed push recovery is automatic and bounded;
- failed or expired scans cannot alter active digital workers.

Open objections:

- no managed configuration, pairing, rotation, removal or health UI exists;
- there is no customer-facing progress/diagnostics/preview flow;
- config-summary attestation and recovery copy need product design.

## Test expert review

Score: 2/3

Accepted evidence:

- tests cover private permissions, symlink config/root, outside-root rejection, invalid adapter/config/version, successful AgentWaker scan through the generic registry, poll throttling and capacity;
- an in-memory transport verifies heartbeat negotiation, lease renewal and terminal result paths/bodies without opening a listener;
- ordinary and targeted race tests cover daemon, handler, registry and adapter code; sqlc generation and vet pass;
- a live-PostgreSQL workspace deletion test is present and checks every role-source table, but skips when PostgreSQL is unavailable.

Open objections:

- live lease renewal, expiry, concurrent claim, duplicate report and workspace-delete/mutation races require PostgreSQL integration runs;
- full listener-based daemon and WebSocket package tests pass with approved local-loopback access;
- long-running 10 GiB source, slow disk, network filesystem, daemon restart and report-outage fault injection are absent;
- no 100-concurrent-scan or 10,000-user capacity result exists.

## CEO review

Score: 2/3

Accepted evidence:

- this closes the first real source-to-secret-free-snapshot loop while preserving the generic adapter boundary;
- manual local opt-in, server flags and independent apply disablement sharply limit blast radius;
- throttling and bounded concurrency address obvious fleet-cost multipliers before load testing.

Open objections:

- manual configuration is not a saleable workflow;
- no measured latency, infrastructure cost, support load or customer outcome exists;
- artifact transfer, preview/apply and a second adapter remain necessary before ecosystem value is proven.

## Security and rollout blockers

- apply and secret transfer remain disabled;
- managed config lifecycle, config-summary attestation and platform-specific file/ACL evidence are required;
- live cross-tenant, lease-contention, teardown-race and failover tests must pass;
- artifact content and retention policy must be complete before materialization;
- rollout score remains below 3 for every perspective.

Final decision: CONDITIONAL
