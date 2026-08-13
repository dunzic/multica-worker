package rolesource

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoleSourceChartIsClosedByDefaultAndJobsAreExplicit(t *testing.T) {
	chart := filepath.Join("..", "..", "..", "deploy", "helm", "multica")
	values := readRoleSourceChartContract(t, filepath.Join(chart, "values.yaml"))
	for _, required := range []string{
		"roleSource:\n", "syncEnabled: false", "scanEnabled: false", "applyEnabled: false",
		"artifactGCEnabled: false", "artifactIntegrityEnabled: false", "retentionEnabled: false", "capacityEvidence:\n",
		"disasterRecovery:\n", "backup:\n", "enabled: false", "runName:", "existingClaim:", "signingSecretName:",
	} {
		if !strings.Contains(values, required) {
			t.Errorf("values.yaml omits default-safe role-source contract %q", required)
		}
	}
	config := readRoleSourceChartContract(t, filepath.Join(chart, "templates", "configmap.yaml"))
	for _, required := range []string{
		"FF_ROLE_SOURCE_SYNC", "FF_ROLE_SOURCE_SCAN", "FF_ROLE_SOURCE_APPLY",
		"MULTICA_ROLE_SOURCE_ARTIFACT_GC_ENABLED", "MULTICA_ROLE_SOURCE_ARTIFACT_INTEGRITY_ENABLED", "MULTICA_ROLE_SOURCE_RETENTION_ENABLED",
	} {
		if !strings.Contains(config, required) {
			t.Errorf("config map omits role-source gate %q", required)
		}
	}
	jobs := readRoleSourceChartContract(t, filepath.Join(chart, "templates", "role-source-jobs.yaml"))
	for _, required := range []string{
		"if .Values.roleSource.capacityEvidence.enabled", "if .Values.roleSource.disasterRecovery.backup.enabled",
		"backoffLimit: 0", "restartPolicy: Never", "./role_source_capacity", "./role_source_dr",
		"default_transaction_read_only=on", "MULTICA_ROLE_SOURCE_DR_SIGNING_PRIVATE_KEY", "secretKeyRef:",
		"runName is required", "existingClaim is required", "requires non-zero approved", "must be a safe", "sha256sum | trunc 8",
	} {
		if !strings.Contains(jobs, required) {
			t.Errorf("role-source Job template omits safety contract %q", required)
		}
	}
	if strings.Count(jobs, "name: {{ .Values.existingSecret }}") != 4 {
		t.Fatalf("role-source Jobs must reference only built-in password or external URL keys from existingSecret")
	}
	for _, forbidden := range []string{
		"BASE64_ED25519", "VALIDATION_PASSWORD", "AWS_SECRET_ACCESS_KEY=",
		"secretRef:\n                name: {{ .Values.existingSecret }}",
	} {
		if strings.Contains(jobs, forbidden) {
			t.Errorf("role-source Job template contains broad/inline secret contract %q", forbidden)
		}
	}
}

func readRoleSourceChartContract(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
