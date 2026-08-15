package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestDecodeRoleSourceTaskPinKeepsOnlyDigestProvenance(t *testing.T) {
	row := db.RoleSourceTaskPin{
		SourceID:         pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		SourceRoleID:     "writer",
		SnapshotDigest:   "sha256:" + strings.Repeat("a", 64),
		RoleObjectDigest: "sha256:" + strings.Repeat("b", 64),
		CapabilityPins:   []byte(`[{"capability_id":"browser","skill_id":"draft","target_skill_id":"00000000-0000-4000-8000-000000000099","profile":"default","version_constraint":"^1.0.0","resolved_version":"1.2.3","object_digest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","permission_mode":"read-only","required":true,"fallback":"blocked"}]`),
	}
	pin, err := decodeRoleSourceTaskPin(row)
	if err != nil {
		t.Fatal(err)
	}
	if pin.SourceRoleID != "writer" || len(pin.CapabilityPins) != 1 || pin.CapabilityPins[0].ResolvedVersion != "1.2.3" || pin.CapabilityPins[0].TargetSkillID == "" || pin.CapabilityPins[0].Fallback != "blocked" {
		t.Fatalf("decoded pin = %+v", pin)
	}
	body, err := json.Marshal(pin)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"custom_env", "mcp_config", "secret", "instructions", "artifact_body"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("wire pin contains forbidden field %q: %s", forbidden, body)
		}
	}
}

func TestListRoleSourceTaskPinsRejectsUnboundedOrAmbiguousCursor(t *testing.T) {
	for _, test := range []struct {
		name, query, message string
	}{
		{name: "zero limit", query: "?limit=0", message: "limit must be between 1 and 100"},
		{name: "oversized limit", query: "?limit=101", message: "limit must be between 1 and 100"},
		{name: "partial cursor", query: "?before_created_at=2026-08-13T00%3A00%3A00Z", message: "must be set together"},
		{name: "invalid cursor time", query: "?before_created_at=nope&before_task_id=00000000-0000-4000-8000-000000000001", message: "invalid before_created_at"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := &fakeRoleSourceControlPlane{}
			h := roleSourceTestHandler(t, true, fake)
			request := withURLParams(
				newRequestAs(testUserID, http.MethodGet, "/ignored"+test.query, nil),
				"id", testWorkspaceID, "sourceId", roleSourceTestSourceID,
			)
			response := httptest.NewRecorder()
			h.ListRoleSourceTaskPins(response, request)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), test.message) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if fake.calls != 1 {
				t.Fatalf("control-plane calls=%d, want source scope check only", fake.calls)
			}
		})
	}
}

func TestDecodeRoleSourceTaskPinRejectsMalformedCapabilityEvidence(t *testing.T) {
	_, err := decodeRoleSourceTaskPin(db.RoleSourceTaskPin{CapabilityPins: []byte(`{"not":"an array"}`)})
	if err == nil {
		t.Fatal("malformed capability evidence accepted")
	}
}
