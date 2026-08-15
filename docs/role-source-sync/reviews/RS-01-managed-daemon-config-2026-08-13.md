# RS-01 managed daemon configuration review

Date: 2026-08-13

Gate: design and merge evidence

Final decision: **GO for merge behind disabled role-source flags; NO-GO for broad customer self-service**

## Customer and product outcome

An operator no longer hand-edits the daemon's private key-bearing file. They can inspect a redacted status plus revision, apply a complete secret-free desired state with an exact compare-and-swap token, add or remove source IDs through that document, and rotate AgentWaker evidence material only through a separately confirmed action. A malformed or missing-root configuration still yields a recovery revision instead of trapping the operator in an unfixable state.

## Architecture review — 2/3

- The public desired-state contract omits `digest_key`; the manager generates, preserves, clears and rotates the private 32-byte value.
- Publication requires a canonical absolute target under an existing directory that is not group/world writable, a private non-symlink lock, exact full-file revision, validation of every registered adapter, same-directory temporary file, file sync, atomic replace and directory sync.
- The profile-local default is discovered automatically; an explicit environment path remains the operator override. No source path or credential enters the control plane.
- Invalid existing JSON can be replaced by its exact raw-body revision. Regenerating an unrecoverable AgentWaker key marks rescan required.
- Open objections: the daemon does not hot-reload the file, there is no server-side attestation that a runtime currently loaded the shown revision, and signed/remote configuration remains outside this slice.

## Product review — 2/3

- `show`, `apply`, and `rotate-key` match the three real operator intents and make stale edits explicit rather than silently overwriting another administrator.
- Source removal is declarative: remove the ID from the full desired document. Removing the final AgentWaker source also removes unnecessary key material.
- Output is intentionally redacted to file/root basenames, source IDs/kinds and adapter-approved attributes. Terminal control characters are neutralized in table output.
- Open objections: JSON plus revision tokens are still an expert workflow; there is no guided form, dry-run diff, automatic daemon restart/rescan, or explicit disable-last-source product flow.

## Test review — 2/3

- Tests cover create/show/update, automatic profile discovery, source addition/removal, key generation/preservation/rotation, stale revision, active lock, malformed-file recovery, invalid roots, symlink and public input rejection, terminal-safe output, managed-task refusal and injected replace failure with temporary-file cleanup.
- The original daemon scanner tests continue to validate private-file mode, secure open, allowed-root confinement and adapter composition.
- Both daemon and CLI packages cross-compile for Windows with the platform locking and replace implementation.
- Open objections: live Windows execution, kill/power-loss injection between sync/rename/directory-sync, network filesystems, daemon restart/hot-reload and concurrent multi-process stress remain unproven.

## CEO review — 2/3

- This removes a high-support, high-liability step from every adapter onboarding and keeps future adapter configuration behind one source-neutral lifecycle.
- Mandatory revision checks and recoverable status reduce the chance that an operator loses a working fleet configuration during rollout.
- Open objections: measure onboarding time, failed configuration rate and recovery time with pilot teams before assigning commercial value; a CLI-only expert workflow is not yet a marketplace onboarding experience.

## Security, privacy and data-loss decision

- Private key material is neither accepted nor returned by the managed command contract.
- Desired-state files must be private regular files, or may arrive through bounded stdin; raw adapter JSON is never placed in process arguments.
- Commands reject daemon-managed agent tasks so a worker cannot mutate its owner's local source authority through the supported CLI.
- Stale and concurrent writers fail before publish. Invalid desired state never replaces the last-known-good file. A key rotation or forced regeneration is visibly marked for rescan.
- Broad rollout remains blocked on live cross-platform failure exercises, guided authorization UX and runtime-loaded-revision attestation.

## Rollout decision

Merge is allowed behind `role_source_sync` and `role_source_scan`. Pilot operators may use the CLI after a backup and restart/rescan runbook is supplied. Customer-facing configuration remains disabled.
