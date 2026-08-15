# RS-04 indexed capability materialization review

Date: 2026-08-13

Final decision: **GO to merge behind default-off controlled apply; NO-GO for
production until live end-to-end scale evidence passes.**

## Architecture review — 2/3

The apply transaction now constructs one immutable capability-ID index from the
already validated snapshot. Capability binding and skill-file preparation both
reuse it. The former linear search repeated once per binding could degrade to
quadratic CPU at the 10,000-capability/10,000-binding contract limit before any
database write; lookup is now linear to build and constant-time per binding.
The index contains normalized non-secret capability definitions only and does
not change ownership, permission or adapter boundaries.

## Product review — 2/3

Large capability catalogs should spend less time in the locked apply path and
have more predictable completion times. No customer-visible contract changes.
Measured staging latency is still required before making an SLO claim.

## Test review — 2/3

A 10,000-capability reverse-order lookup fixture proves the index is built once
and resolves the full set. Focused role-source and race tests pass. Missing:
CPU/allocation profiles inside a live 1,000-role/10,000-skill apply.

## CEO review — 2/3

This is a low-risk removal of a deterministic enterprise-scale timeout hazard.
It improves pilot confidence but is not standalone market value and does not
change the production NO-GO without live PostgreSQL, object-store and recovery
evidence.
