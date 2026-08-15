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
require_rendered_value "$backup_job" 'value: "private_key"'
require_rendered_value "$backup_job" 'claimName: "role-source-backup"'
if grep -Fq 'BASE64_' <<<"$backup_job"; then
  echo "Rendered role-source Job contains inline secret material"
  exit 1
fi

kms_backup_job="$(
  helm template multica "$CHART_DIR" \
    --show-only templates/role-source-jobs.yaml \
    --set backend.uploads.persistence.enabled=false \
    --set postgres.external.enabled=true \
    --set roleSource.jobs.serviceAccountName=multica-role-source-backup \
    --set roleSource.disasterRecovery.backup.enabled=true \
    --set roleSource.disasterRecovery.backup.runName=drill-kms-20260815 \
    --set roleSource.disasterRecovery.backup.existingClaim=role-source-backup \
    --set roleSource.disasterRecovery.backup.outputDirectory=drill-kms-20260815 \
    --set roleSource.disasterRecovery.backup.signingProvider=aws_kms \
    --set roleSource.disasterRecovery.backup.signerKeyId=backup-kms-v1 \
    --set roleSource.disasterRecovery.backup.awsKmsKeyId=arn:aws:kms:us-east-1:111122223333:key/00000000-0000-4000-8000-000000000001 \
    --set roleSource.disasterRecovery.backup.signingPublicKey=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA= \
    --set roleSource.disasterRecovery.backup.storageBucket=multica-role-source-backups \
    --set roleSource.disasterRecovery.backup.storageRegion=us-east-1 \
    --set roleSource.disasterRecovery.backup.storageEndpointUrl=https://objects.example.com \
    --set roleSource.disasterRecovery.backup.storageUsePathStyle=true
)"
require_rendered_value "$kms_backup_job" 'serviceAccountName: "multica-role-source-backup"'
require_rendered_value "$kms_backup_job" 'value: "aws_kms"'
require_rendered_value "$kms_backup_job" 'value: "backup-kms-v1"'
require_rendered_value "$kms_backup_job" 'value: "arn:aws:kms:us-east-1:111122223333:key/00000000-0000-4000-8000-000000000001"'
require_rendered_value "$kms_backup_job" 'value: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="'
require_rendered_value "$kms_backup_job" 'name: AWS_REGION'
require_rendered_value "$kms_backup_job" 'value: "multica-role-source-backups"'
require_rendered_value "$kms_backup_job" 'name: S3_ENDPOINT_URL'
require_rendered_value "$kms_backup_job" 'value: "https://objects.example.com"'
require_rendered_value "$kms_backup_job" 'name: S3_USE_PATH_STYLE'
require_rendered_value "$kms_backup_job" 'value: "true"'
if grep -Fq 'MULTICA_ROLE_SOURCE_DR_SIGNING_PRIVATE_KEY' <<<"$kms_backup_job"; then
  echo "AWS KMS backup Job rendered a raw signing private-key variable"
  exit 1
fi
if grep -Fq 'name: "role-source-dr-signer"' <<<"$kms_backup_job"; then
  echo "AWS KMS backup Job rendered the legacy signing secret"
  exit 1
fi
if grep -Fq 'AWS_ENDPOINT_URL' <<<"$kms_backup_job"; then
  echo "AWS KMS backup Job rendered a global AWS endpoint override"
  exit 1
fi

if helm template multica "$CHART_DIR" \
  --show-only templates/role-source-jobs.yaml \
  --set postgres.external.enabled=true \
  --set roleSource.disasterRecovery.backup.enabled=true \
  --set roleSource.disasterRecovery.backup.runName=drill-kms-20260815 \
  --set roleSource.disasterRecovery.backup.existingClaim=role-source-backup \
  --set roleSource.disasterRecovery.backup.outputDirectory=drill-kms-20260815 \
  --set roleSource.disasterRecovery.backup.signingProvider=aws_kms \
  --set roleSource.disasterRecovery.backup.signerKeyId=backup-kms-v1 \
  --set roleSource.disasterRecovery.backup.awsKmsKeyId=arn:aws:kms:us-east-1:111122223333:key/00000000-0000-4000-8000-000000000001 \
  --set roleSource.disasterRecovery.backup.signingPublicKey=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA= \
  >/dev/null 2>&1; then
  echo "AWS KMS backup Job rendered without workload identity service account"
  exit 1
fi

if helm template multica "$CHART_DIR" \
  --show-only templates/role-source-jobs.yaml \
  --set postgres.external.enabled=true \
  --set roleSource.jobs.serviceAccountName=multica-role-source-backup \
  --set roleSource.disasterRecovery.backup.enabled=true \
  --set roleSource.disasterRecovery.backup.runName=drill-kms-20260815 \
  --set roleSource.disasterRecovery.backup.existingClaim=role-source-backup \
  --set roleSource.disasterRecovery.backup.outputDirectory=drill-kms-20260815 \
  --set roleSource.disasterRecovery.backup.signingProvider=aws_kms \
  --set roleSource.disasterRecovery.backup.signerKeyId=backup-kms-v1 \
  --set roleSource.disasterRecovery.backup.awsKmsKeyId=arn:aws:kms:us-east-1:111122223333:key/00000000-0000-4000-8000-000000000001 \
  --set roleSource.disasterRecovery.backup.signingPublicKey=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA= \
  --set roleSource.disasterRecovery.backup.storageSecretName=static-s3-credentials \
  >/dev/null 2>&1; then
  echo "AWS KMS backup Job accepted a static storage credential Secret"
  exit 1
fi

if helm template multica "$CHART_DIR" \
  --show-only templates/role-source-jobs.yaml \
  --set postgres.external.enabled=true \
  --set roleSource.jobs.serviceAccountName=multica-role-source-backup \
  --set roleSource.disasterRecovery.backup.enabled=true \
  --set roleSource.disasterRecovery.backup.runName=drill-kms-20260815 \
  --set roleSource.disasterRecovery.backup.existingClaim=role-source-backup \
  --set roleSource.disasterRecovery.backup.outputDirectory=drill-kms-20260815 \
  --set roleSource.disasterRecovery.backup.signingProvider=aws_kms \
  --set roleSource.disasterRecovery.backup.signerKeyId=backup-kms-v1 \
  --set roleSource.disasterRecovery.backup.awsKmsKeyId=arn:aws:kms:us-east-1:111122223333:key/00000000-0000-4000-8000-000000000001 \
  --set roleSource.disasterRecovery.backup.signingPublicKey=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA= \
  --set roleSource.disasterRecovery.backup.storageBucket=multica-role-source-backups \
  --set roleSource.disasterRecovery.backup.storageRegion=us-east-1 \
  --set roleSource.disasterRecovery.backup.storageEndpointUrl=http://objects.example.com \
  >/dev/null 2>&1; then
  echo "AWS KMS backup Job accepted a non-TLS storage endpoint"
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
