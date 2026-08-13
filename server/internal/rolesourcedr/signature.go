package rolesourcedr

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/google/uuid"
)

const manifestSignatureDomain = "multica-role-source-disaster-recovery-manifest-v1\x00"

var (
	sha256Pattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	keyIDPattern  = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,99}$`)
)

func ValidateManifest(manifest Manifest, allowUnsigned bool) error {
	if manifest.ContractVersion != ContractVersion {
		return fmt.Errorf("unsupported DR manifest contract %q", manifest.ContractVersion)
	}
	if _, err := uuid.Parse(manifest.BackupID); err != nil || manifest.CreatedAt.IsZero() {
		return errors.New("invalid DR manifest identity")
	}
	if manifest.DatabaseMajorVersion != 17 || manifest.MigrationCount <= 0 || !sha256Pattern.MatchString(manifest.MigrationRoot) {
		return errors.New("invalid DR database identity")
	}
	if len(manifest.Tables) != len(tableNames) {
		return errors.New("incomplete DR table inventory")
	}
	for index, table := range manifest.Tables {
		if table.Name != tableNames[index] || table.RowCount < 0 || !sha256Pattern.MatchString(table.Commitment) {
			return errors.New("invalid DR table inventory")
		}
	}
	if manifest.Artifacts.Count < 0 || manifest.Artifacts.SizeBytes < 0 || !sha256Pattern.MatchString(manifest.Artifacts.Commitment) ||
		!sha256Pattern.MatchString(manifest.DatabaseDumpDigest) || !sha256Pattern.MatchString(manifest.ArtifactArchiveDigest) {
		return errors.New("invalid DR file or artifact commitment")
	}
	for _, keyID := range manifest.KeyRequirements.SecretTransferKeyIDs {
		if !keyIDPattern.MatchString(keyID) {
			return errors.New("invalid DR secret key requirement")
		}
	}
	for index := 1; index < len(manifest.KeyRequirements.SecretTransferKeyIDs); index++ {
		if manifest.KeyRequirements.SecretTransferKeyIDs[index-1] >= manifest.KeyRequirements.SecretTransferKeyIDs[index] {
			return errors.New("DR secret key requirements must be sorted and unique")
		}
	}
	if manifest.KeyRequirements.PendingTransfers < 0 || manifest.KeyRequirements.SubmittedTransfers < 0 {
		return errors.New("invalid DR secret transfer count")
	}
	if manifest.Signature == "" || manifest.SignerKeyID == "" {
		if allowUnsigned && manifest.UnsignedDevelopment && manifest.Signature == "" && manifest.SignerKeyID == "" {
			return nil
		}
		return errors.New("DR manifest signature is required")
	}
	if !keyIDPattern.MatchString(manifest.SignerKeyID) {
		return errors.New("invalid DR manifest signer key id")
	}
	signature, err := base64.RawStdEncoding.DecodeString(manifest.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return errors.New("invalid DR manifest signature encoding")
	}
	return nil
}

func SignManifest(manifest *Manifest, keyID string, privateKey ed25519.PrivateKey) error {
	if manifest == nil || !keyIDPattern.MatchString(keyID) || len(privateKey) != ed25519.PrivateKeySize {
		return errors.New("valid DR signer key id and Ed25519 private key are required")
	}
	manifest.SignerKeyID = keyID
	manifest.Signature = ""
	manifest.UnsignedDevelopment = false
	body, err := manifestSignatureBody(*manifest)
	if err != nil {
		return err
	}
	manifest.Signature = base64.RawStdEncoding.EncodeToString(ed25519.Sign(privateKey, body))
	return ValidateManifest(*manifest, false)
}

// GenerateSigningKey is an operator bootstrap helper. Private bytes are
// returned only to the caller for immediate KMS/HSM import and are never
// persisted by the DR package.
func GenerateSigningKey() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return ed25519.GenerateKey(rand.Reader)
}

func ValidateSignerKeyID(keyID string) error {
	if !keyIDPattern.MatchString(keyID) {
		return errors.New("invalid DR signer key id")
	}
	return nil
}

func VerifyManifestSignature(manifest Manifest, trusted map[string]ed25519.PublicKey, allowUnsigned bool) error {
	if err := ValidateManifest(manifest, allowUnsigned); err != nil {
		return err
	}
	if manifest.Signature == "" {
		return nil
	}
	publicKey, ok := trusted[manifest.SignerKeyID]
	if !ok || len(publicKey) != ed25519.PublicKeySize {
		return errors.New("DR manifest signer is not trusted")
	}
	signature, _ := base64.RawStdEncoding.DecodeString(manifest.Signature)
	body, err := manifestSignatureBody(manifest)
	if err != nil {
		return err
	}
	if !ed25519.Verify(publicKey, body, signature) {
		return errors.New("DR manifest signature verification failed")
	}
	return nil
}

func manifestSignatureBody(manifest Manifest) ([]byte, error) {
	manifest.Signature = ""
	body, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	return append([]byte(manifestSignatureDomain), body...), nil
}
