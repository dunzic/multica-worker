package rolesource

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type trackedArtifactReader struct {
	bodies      map[string][]byte
	delay       time.Duration
	mu          sync.Mutex
	active, max int
}

func (r *trackedArtifactReader) GetReader(ctx context.Context, storageKey string) (io.ReadCloser, error) {
	r.mu.Lock()
	r.active++
	if r.active > r.max {
		r.max = r.active
	}
	r.mu.Unlock()
	timer := time.NewTimer(r.delay)
	select {
	case <-ctx.Done():
		if !timer.Stop() {
			<-timer.C
		}
		r.closed()
		return nil, ctx.Err()
	case <-timer.C:
	}
	body, ok := r.bodies[storageKey]
	if !ok {
		r.closed()
		return nil, fmt.Errorf("missing body %s", storageKey)
	}
	return &trackedReadCloser{Reader: bytes.NewReader(body), close: r.closed}, nil
}

func (r *trackedArtifactReader) closed() {
	r.mu.Lock()
	r.active--
	r.mu.Unlock()
}

type trackedReadCloser struct {
	*bytes.Reader
	close func()
	once  sync.Once
}

func (r *trackedReadCloser) Close() error {
	r.once.Do(r.close)
	return nil
}

func TestCollectArtifactRefsCanonicalizesAndRejectsDigestSizeConflicts(t *testing.T) {
	manifest := planTestManifest()
	manifest.Roles[0].Skills[0].Entrypoint.Digest = testSHA256("b")
	snapshot := planTestSnapshot(t, manifest)
	refs, err := CollectArtifactRefs(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 || refs[0].Digest >= refs[1].Digest {
		t.Fatalf("canonical artifact refs = %+v", refs)
	}

	conflictManifest := planTestManifest()
	conflictManifest.Roles[0].Skills[0].Entrypoint.Digest = conflictManifest.Roles[0].Instructions.Digest
	conflictManifest.Roles[0].Skills[0].Entrypoint.SizeBytes = conflictManifest.Roles[0].Instructions.SizeBytes + 1
	conflicting := planTestSnapshot(t, conflictManifest)
	if _, err := CollectArtifactRefs(conflicting); err == nil {
		t.Fatal("artifact collector accepted one digest with conflicting sizes")
	}
}

func TestCollectMaterializationArtifactRefsLoadsSkillFilesButExcludesUnboundCapabilities(t *testing.T) {
	manifest := planTestManifest()
	manifest.Roles[0].Skills[0].Entrypoint.Digest = testSHA256("b")
	profile := testArtifact("roles/writer/profile.md")
	profile.Digest = testSHA256("c")
	manifest.Roles[0].Profile = &profile
	supporting := testArtifact("roles/writer/skills/draft/helper.md")
	supporting.Digest = testSHA256("d")
	manifest.Roles[0].Skills[0].Artifacts = []ArtifactRef{supporting}
	prompt := testArtifact("roles/writer/automations/daily.md")
	prompt.Digest = testSHA256("e")
	manifest.Roles[0].Automations = []Automation{{
		ID: "daily", Name: "Daily", Schedule: "0 9 * * *", Timezone: "UTC", Prompt: prompt,
	}}
	capabilityEntrypoint := testArtifact("capabilities/browser/SKILL.md")
	capabilityEntrypoint.Digest = testSHA256("f")
	capabilitySupporting := testArtifact("capabilities/browser/helper.md")
	capabilitySupporting.Digest = testSHA256("0")
	manifest.Capabilities = []Capability{{
		ID: "browser", Name: "Browser", Version: "1.0.0", Entrypoint: capabilityEntrypoint,
		Artifacts: []ArtifactRef{capabilitySupporting},
	}}
	snapshot := planTestSnapshot(t, manifest)
	all, err := CollectArtifactRefs(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	materialized, err := collectMaterializationArtifactRefs(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 7 || len(materialized) != 4 {
		t.Fatalf("artifact refs: all=%d materialized=%d", len(all), len(materialized))
	}
	want := map[string]bool{
		manifest.Roles[0].Instructions.Digest:         true,
		manifest.Roles[0].Skills[0].Entrypoint.Digest: true,
		supporting.Digest:                             true,
		prompt.Digest:                                 true,
	}
	for _, ref := range materialized {
		if !want[ref.Digest] {
			t.Fatalf("unbound package %s was loaded into atomic apply", ref.Path)
		}
	}
}

func TestVerifyMaterializationArtifactBodiesUsesBoundedParallelReads(t *testing.T) {
	const count = 64
	refs := make([]ArtifactRef, count)
	ledger := make(map[string]db.RoleSourceArtifact, count)
	bodies := make(map[string][]byte, count)
	for index := range refs {
		body := []byte(fmt.Sprintf("artifact-%03d", index))
		digest := sha256.Sum256(body)
		digestText := "sha256:" + hex.EncodeToString(digest[:])
		storageKey := fmt.Sprintf("artifact/%03d", index)
		refs[index] = ArtifactRef{Digest: digestText, Path: storageKey + ".md", MediaType: "text/markdown", SizeBytes: int64(len(body))}
		ledger[digestText] = db.RoleSourceArtifact{Digest: digestText, SizeBytes: int64(len(body)), StorageKey: storageKey}
		bodies[storageKey] = body
	}
	reader := &trackedArtifactReader{bodies: bodies, delay: 2 * time.Millisecond}
	control := &ControlPlane{artifacts: reader}
	verified, err := control.verifyMaterializationArtifactBodies(context.Background(), refs, ledger)
	if err != nil {
		t.Fatal(err)
	}
	if len(verified) != count {
		t.Fatalf("verified artifacts=%d, want=%d", len(verified), count)
	}
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if reader.max <= 1 || reader.max > maxConcurrentArtifactReads || reader.active != 0 {
		t.Fatalf("parallel reads: max=%d active=%d limit=%d", reader.max, reader.active, maxConcurrentArtifactReads)
	}
}

func TestScanLeaseIsActiveRequiresExactUnexpiredOwner(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	runtimeID := util.MustParseUUID("00000000-0000-4000-8000-000000000071")
	leaseToken := util.MustParseUUID("00000000-0000-4000-8000-000000000072")
	controlPlane := &ControlPlane{now: func() time.Time { return now }}
	source := db.RoleSource{RuntimeID: runtimeID}
	request := db.RoleSourceScanRequest{
		Status: "claimed", ClaimedByRuntimeID: runtimeID, LeaseToken: leaseToken,
		LeaseExpiresAt: pgtype.Timestamptz{Time: now.Add(time.Minute), Valid: true},
	}
	if !controlPlane.scanLeaseIsActive(source, request, runtimeID, leaseToken) {
		t.Fatal("exact unexpired owner was rejected")
	}
	request.LeaseExpiresAt.Time = now
	if controlPlane.scanLeaseIsActive(source, request, runtimeID, leaseToken) {
		t.Fatal("expired lease was accepted")
	}
}
