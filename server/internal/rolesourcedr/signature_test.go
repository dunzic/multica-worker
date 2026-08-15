package rolesourcedr

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
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
	if manifest.SignatureScheme != SignatureSchemeEd25519CommitmentV2 {
		t.Fatalf("signature scheme = %q", manifest.SignatureScheme)
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

func TestLegacyManifestSignatureRemainsVerifiable(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manifest := validTestManifest()
	manifest.SignerKeyID = "backup-v1"
	message, err := manifestSignatureMessage(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Signature = base64.RawStdEncoding.EncodeToString(ed25519.Sign(privateKey, message))
	if err := VerifyManifestSignature(manifest, map[string]ed25519.PublicKey{"backup-v1": publicKey}, false); err != nil {
		t.Fatalf("legacy manifest rejected: %v", err)
	}
}

func TestManifestSignatureSchemeCannotBeDowngradedOrSubstituted(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manifest := validTestManifest()
	if err := SignManifest(&manifest, "backup-v2", privateKey); err != nil {
		t.Fatal(err)
	}

	downgraded := manifest
	downgraded.SignatureScheme = ""
	if err := VerifyManifestSignature(downgraded, map[string]ed25519.PublicKey{"backup-v2": publicKey}, false); err == nil {
		t.Fatal("signature-scheme downgrade accepted")
	}
	unknown := manifest
	unknown.SignatureScheme = "future-unknown-scheme"
	if err := VerifyManifestSignature(unknown, map[string]ed25519.PublicKey{"backup-v2": publicKey}, false); err == nil {
		t.Fatal("unknown signature scheme accepted")
	}
	changedSigner := manifest
	changedSigner.SignerKeyID = "backup-v3"
	if err := VerifyManifestSignature(changedSigner, map[string]ed25519.PublicKey{"backup-v3": publicKey}, false); err == nil {
		t.Fatal("signer substitution accepted")
	}
}

func TestPreparedSignatureMessageIsKMSBoundedForLargeManifest(t *testing.T) {
	manifest := validTestManifest()
	canonical, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(canonical) <= 4096 {
		t.Fatalf("test manifest is only %d bytes; it does not exercise the KMS RAW limit", len(canonical))
	}
	message, err := PrepareManifestSignature(&manifest, "backup-v2")
	if err != nil {
		t.Fatal(err)
	}
	if len(message) >= 4096 {
		t.Fatalf("KMS signing message size = %d", len(message))
	}
	if len(message) != len(manifestSignatureDomainV2)+sha512.Size {
		t.Fatalf("KMS signing message size = %d, want %d", len(message), len(manifestSignatureDomainV2)+sha512.Size)
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

func TestManifestSignatureMessageIsDomainSeparatedAndBindsSigner(t *testing.T) {
	manifest := validTestManifest()
	message, err := PrepareManifestSignature(&manifest, "backup-v1")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(message, []byte(manifestSignatureDomainV2)) {
		t.Fatal("signature message is not domain separated")
	}
	changed := manifest
	changed.SignerKeyID = "backup-v2"
	changedMessage, err := manifestSignatureMessage(changed)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(message, changedMessage) {
		t.Fatal("signer identity is not committed")
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
