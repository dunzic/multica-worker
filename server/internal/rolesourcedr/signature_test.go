package rolesourcedr

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestManifestSignatureDetectsReplacementAndUnknownKey(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manifest := validTestManifest()
	if err := SignManifest(&manifest, "backup-v1", privateKey); err != nil {
		t.Fatal(err)
	}
	if err := VerifyManifestSignature(manifest, map[string]ed25519.PublicKey{"backup-v1": publicKey}, false); err != nil {
		t.Fatal(err)
	}
	manifest.Artifacts.SizeBytes++
	if err := VerifyManifestSignature(manifest, map[string]ed25519.PublicKey{"backup-v1": publicKey}, false); err == nil {
		t.Fatal("tampered manifest signature accepted")
	}
	manifest.Artifacts.SizeBytes--
	if err := VerifyManifestSignature(manifest, nil, false); err == nil {
		t.Fatal("unknown signer accepted")
	}
}

func TestUnsignedManifestRequiresExplicitOverride(t *testing.T) {
	manifest := validTestManifest()
	if err := VerifyManifestSignature(manifest, nil, false); err == nil {
		t.Fatal("unsigned manifest accepted by default")
	}
	manifest.UnsignedDevelopment = true
	if err := VerifyManifestSignature(manifest, nil, true); err != nil {
		t.Fatal(err)
	}
}

func TestManifestSignatureBodyIsDomainSeparatedAndExcludesOnlySignature(t *testing.T) {
	manifest := validTestManifest()
	manifest.SignerKeyID = "backup-v1"
	body, err := manifestSignatureBody(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(body, []byte(manifestSignatureDomain)) {
		t.Fatal("signature body is not domain separated")
	}
	if !bytes.Contains(body, []byte(`"signer_key_id":"backup-v1"`)) {
		t.Fatal("signer identity is not signed")
	}
}

func validTestManifest() Manifest {
	tables := make([]TableSummary, len(tableNames))
	for i, name := range tableNames {
		tables[i] = TableSummary{Name: name, Commitment: digestBytes(nil)}
	}
	return Manifest{ContractVersion: ContractVersion, BackupID: uuid.NewString(), CreatedAt: time.Now().UTC(), DatabaseMajorVersion: 17,
		MigrationCount: 1, MigrationRoot: digestBytes([]byte("migration")), Tables: tables,
		Artifacts: ArtifactSummary{Commitment: digestBytes(nil)}, DatabaseDumpDigest: digestBytes([]byte("dump")), ArtifactArchiveDigest: digestBytes([]byte("archive"))}
}
