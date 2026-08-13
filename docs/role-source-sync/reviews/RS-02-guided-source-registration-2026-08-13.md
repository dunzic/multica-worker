# RS-02 guided source registration review

Date: 2026-08-13

Decision: **GO behind the existing default-off Role Source scan flag.**

## Product outcome

Workspace owners and admins can register a source without copying Runtime UUIDs
or adapter versions by hand. The flow lists workspace Runtimes, offers only the
server's compile-time trusted adapter descriptors, and submits the selected
descriptor version with a source display name and daemon-local config handle.

The browser does not author private source configuration. It has no path,
credential, URL, signing-key, token or source-content field. Operators first
create that configuration on the Runtime, then bind its non-secret handle. The
new source remains unattested until the daemon reports matching loaded evidence,
and the read-only scan action stays disabled until status is `loaded`.

## Contract and failure safety

- adapter list and create responses use strict bounded schemas;
- malformed responses fail closed to an empty adapter list or null source;
- registration sends an empty redacted summary, never a fabricated path label;
- an invalid create response warns that the write may have committed and tells
  the operator to refresh before retrying;
- ordinary members retain read-only visibility but cannot open the registration
  flow.

## Evidence

- Core API tests cover trusted descriptor parsing, exact request fields,
  sensitive-field absence and malformed-response fallback;
- settings tests cover the guided flow, exact payload and loaded-evidence scan
  gate;
- focused Core test: 85 passed;
- focused settings test: 29 passed;
- changed-file ESLint passed;
- workspace typecheck remains blocked by three pre-existing Chat Quick Actions
  cache-state errors outside this change; no new Role Source type error appears.

## Remaining gap

The product still needs a Runtime-local configuration authoring or deep-link
experience. Until that exists, this flow must explain where private configuration
is created; it must never expand into a server-hosted secret/path form.
