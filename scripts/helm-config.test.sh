#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHART_DIR="$ROOT_DIR/deploy/helm/multica"

require_rendered_value() {
  local rendered=$1
  local expected=$2

  if ! grep -Fq "$expected" <<<"$rendered"; then
    echo "Missing expected Helm-rendered config value:"
    echo "  $expected"
    exit 1
  fi
}

helm lint "$CHART_DIR"

default_config="$(
  helm template multica "$CHART_DIR" \
    --show-only templates/configmap.yaml
)"
require_rendered_value "$default_config" 'MULTICA_VCS_INTEGRATION_ENABLED: "true"'
require_rendered_value "$default_config" 'FF_ROLE_SOURCE_SYNC: "false"'
require_rendered_value "$default_config" 'FF_ROLE_SOURCE_SCAN: "false"'
require_rendered_value "$default_config" 'FF_ROLE_SOURCE_APPLY: "false"'
require_rendered_value "$default_config" 'MULTICA_ROLE_SOURCE_ARTIFACT_GC_ENABLED: "false"'
require_rendered_value "$default_config" 'MULTICA_ROLE_SOURCE_ARTIFACT_INTEGRITY_ENABLED: "false"'
require_rendered_value "$default_config" 'MULTICA_ROLE_SOURCE_RETENTION_ENABLED: "false"'

disabled_config="$(
  helm template multica "$CHART_DIR" \
    --show-only templates/configmap.yaml \
    --set backend.config.vcsIntegrationEnabled=false
)"
require_rendered_value "$disabled_config" 'MULTICA_VCS_INTEGRATION_ENABLED: "false"'

role_source_config="$(
  helm template multica "$CHART_DIR" \
    --show-only templates/configmap.yaml \
    --set roleSource.syncEnabled=true \
    --set roleSource.scanEnabled=true \
    --set roleSource.applyEnabled=false
)"
require_rendered_value "$role_source_config" 'FF_ROLE_SOURCE_SYNC: "true"'
require_rendered_value "$role_source_config" 'FF_ROLE_SOURCE_SCAN: "true"'
require_rendered_value "$role_source_config" 'FF_ROLE_SOURCE_APPLY: "false"'

default_chart="$(helm template multica "$CHART_DIR")"
if grep -Eq 'app.kubernetes.io/component: role-source-(capacity|backup)' <<<"$default_chart"; then
  echo "Role-source jobs must not render by default"
  exit 1
fi

capacity_job="$(
  helm template multica "$CHART_DIR" \
    --show-only templates/role-source-jobs.yaml \
    --set roleSource.capacityEvidence.enabled=true \
    --set roleSource.capacityEvidence.runName=capacity-20260813 \
    --set roleSource.capacityEvidence.existingClaim=capacity-evidence \
    --set roleSource.capacityEvidence.workspaceId=00000000-0000-4000-8000-000000000001 \
    --set roleSource.capacityEvidence.runtimeId=00000000-0000-4000-8000-000000000002 \
    --set roleSource.capacityEvidence.minimumWorkspaceMembers=250 \
    --set roleSource.capacityEvidence.minimumWorkspaceRuntimes=100 \
    --set roleSource.capacityEvidence.minimumWorkspaceSources=200 \
    --set roleSource.capacityEvidence.minimumAttestationHistory=100
)"
require_rendered_value "$capacity_job" 'command: ["./role_source_capacity"]'
require_rendered_value "$capacity_job" 'claimName: "capacity-evidence"'
require_rendered_value "$capacity_job" 'backoffLimit: 0'
require_rendered_value "$capacity_job" '"/evidence/role-source-capacity-read.json"'

backup_job="$(
  helm template multica "$CHART_DIR" \
    --show-only templates/role-source-jobs.yaml \
    --set backend.uploads.persistence.enabled=false \
    --set postgres.external.enabled=true \
    --set roleSource.disasterRecovery.backup.enabled=true \
    --set roleSource.disasterRecovery.backup.runName=drill-20260813 \
    --set roleSource.disasterRecovery.backup.existingClaim=role-source-backup \
    --set roleSource.disasterRecovery.backup.outputDirectory=drill-20260813 \
    --set roleSource.disasterRecovery.backup.signingSecretName=role-source-dr-signer
)"
require_rendered_value "$backup_job" 'command: ["./role_source_dr"]'
require_rendered_value "$backup_job" 'app.kubernetes.io/component: role-source-backup'
require_rendered_value "$backup_job" 'name: "role-source-dr-signer"'
require_rendered_value "$backup_job" 'claimName: "role-source-backup"'
if grep -Fq 'BASE64_' <<<"$backup_job"; then
  echo "Rendered role-source Job contains inline secret material"
  exit 1
fi

if helm template multica "$CHART_DIR" \
  --show-only templates/role-source-jobs.yaml \
  --set roleSource.capacityEvidence.enabled=true \
  --set roleSource.capacityEvidence.runName=capacity-20260813 \
  --set roleSource.capacityEvidence.existingClaim=capacity-evidence \
  --set roleSource.capacityEvidence.workspaceId=00000000-0000-4000-8000-000000000001 \
  --set roleSource.capacityEvidence.runtimeId=00000000-0000-4000-8000-000000000002 \
  >/dev/null 2>&1; then
  echo "Capacity Job rendered without approved non-zero cohort minima"
  exit 1
fi

if helm template multica "$CHART_DIR" \
  --show-only templates/role-source-jobs.yaml \
  --set postgres.external.enabled=true \
  --set roleSource.disasterRecovery.backup.enabled=true \
  --set roleSource.disasterRecovery.backup.runName=../escape \
  --set roleSource.disasterRecovery.backup.existingClaim=role-source-backup \
  --set roleSource.disasterRecovery.backup.outputDirectory=../escape \
  --set roleSource.disasterRecovery.backup.signingSecretName=role-source-dr-signer \
  >/dev/null 2>&1; then
  echo "DR backup Job accepted an unsafe output path"
  exit 1
fi

echo "helm config rendering ok"
