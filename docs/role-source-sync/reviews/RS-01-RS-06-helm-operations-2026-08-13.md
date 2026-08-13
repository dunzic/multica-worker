# RS-01/RS-06 Helm operations review — 2026-08-13

Feature: default-closed rollout flags plus explicit capacity and DR backup Jobs
Gate: merge / deployment readiness
Final decision: CONDITIONAL; merge templates, but candidate chart rendering and
cluster execution remain required before any cohort.

| Perspective | Score | Decision and open objections |
| --- | ---: | --- |
| Architecture expert | 2/3 | Product sync/scan/apply flags and destructive retention/GC gates are separately default-off. Capacity and backup are one-shot Jobs using the exact candidate image, explicit PVCs and existing Secrets/workload identity. Restore/verify stays an isolated manual recovery workflow because an ordinary release chart must not be able to overwrite a target database. |
| Product expert | 2/3 | The chart now exposes a repeatable operational entry instead of requiring shell access to a backend pod. Cohort cardinalities and each backup run remain deliberate operator inputs; there is no scheduled backup policy or UI. |
| Test expert | 2/3 | Go static contracts and the repository rendering suite cover closed defaults, explicit gates, Secret references, read-only capacity sessions, no retry, safe output names, successful enabled renders and fail-closed invalid values. The official Helm 3.20.2 archive was verified against its published SHA-256 sidecar before `helm lint` and the full render suite passed; Kubernetes Job execution evidence remains open. |
| CEO | 2/3 | Repeatable, bounded operations reduce rollout and audit cost without widening default customer exposure. SLA claims remain blocked on candidate-cluster capacity, restore, failover and support-labor measurements. |

Security and data-loss conditions:

- the chart never templates key bytes; DR signer and optional storage
  credentials are referenced from separately managed Secrets;
- capacity sessions carry both tool-enforced and `PGOPTIONS` read-only limits;
  the output uses a separate evidence PVC;
- backup uses a unique DNS-safe run name and a safe single output directory on
  a separate backup PVC; local uploads are mounted read-only;
- no restore or verifier Job is templated into ordinary releases. Recovery must
  follow the isolated target and traffic-fence requirements in
  `disaster-recovery.md`.

Rollout decision: keep all five Helm gates false. The candidate chart has passed
local lint and positive/negative rendering; run the capacity
Job on Gate E staging, then run the backup Job plus isolated restore/verify in
Gate F. Scores can reach 3 only after retained evidence matches the candidate
image digest, full commit, PostgreSQL/storage topology and approved cohort.
