package rolesource

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAuditEventFormsTamperEvidentChain(t *testing.T) {
	when := time.Date(2026, 8, 13, 10, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	first, err := BuildAuditEvent("source-1", "workspace-1", 1, "source_registered", AuditActor{Type: "user", ID: "user-1"}, "", AuditPayload{AdapterKind: "fake_source", AdapterVersion: "1.0.0", Result: "succeeded"}, when)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildAuditEvent("source-1", "workspace-1", 2, "scan_succeeded", AuditActor{Type: "runtime", ID: "runtime-1"}, first.EventDigest, AuditPayload{SnapshotDigest: testSHA256("2"), ManifestDigest: testSHA256("3"), Result: "succeeded"}, when.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateAuditEvent(first); err != nil {
		t.Fatal(err)
	}
	if err := ValidateAuditEvent(second); err != nil {
		t.Fatal(err)
	}
	second.Payload.Result = "failed"
	if err := ValidateAuditEvent(second); err == nil {
		t.Fatal("ValidateAuditEvent accepted a changed payload")
	}
}

func TestAuditPayloadIsStructurallySecretFree(t *testing.T) {
	typeOfPayload, err := json.Marshal(AuditPayload{OperationID: "operation-1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"config", "secret", "token", "path", "body", "credential"} {
		if strings.Contains(strings.ToLower(string(typeOfPayload)), forbidden) {
			t.Fatalf("audit payload unexpectedly exposes field %q: %s", forbidden, typeOfPayload)
		}
	}
}

func TestAuditEventRejectsInvalidActorAndDigest(t *testing.T) {
	when := time.Now()
	if _, err := BuildAuditEvent("source-1", "workspace-1", 1, "scan", AuditActor{Type: "system", ID: "not-allowed"}, "", AuditPayload{}, when); err == nil {
		t.Fatal("BuildAuditEvent accepted a system actor id")
	}
	if _, err := BuildAuditEvent("source-1", "workspace-1", 1, "scan", AuditActor{Type: "user", ID: "user-1"}, "plaintext", AuditPayload{}, when); err == nil {
		t.Fatal("BuildAuditEvent accepted a non-digest previous event")
	}
}

func TestAuditEventRejectsNegativeRetentionReclaimProjection(t *testing.T) {
	if _, err := BuildAuditEvent("source-1", "workspace-1", 1, "snapshot_retention_pruned", AuditActor{Type: "system"}, "", AuditPayload{
		UniquelyReclaimableBytes: -1,
	}, time.Now()); err == nil {
		t.Fatal("BuildAuditEvent accepted negative uniquely reclaimable bytes")
	}
}

func TestAuditDigestCanonicalizesEquivalentTimestampZones(t *testing.T) {
	when := time.Date(2026, 8, 14, 1, 2, 3, 456789123, time.UTC)
	event, err := BuildAuditEvent("source-1", "workspace-1", 1, "scan", AuditActor{Type: "system"}, "", AuditPayload{}, when)
	if err != nil {
		t.Fatal(err)
	}
	event.OccurredAt = event.OccurredAt.In(time.FixedZone("CST", 8*60*60))
	if err := ValidateAuditEvent(event); err != nil {
		t.Fatalf("same instant in another zone failed validation: %v", err)
	}
	if event.OccurredAt.Nanosecond() != 456789000 {
		t.Fatalf("audit timestamp was not canonicalized to PostgreSQL microseconds: %s", event.OccurredAt)
	}
}
