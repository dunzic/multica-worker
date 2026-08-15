package rolesourcedr

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/google/uuid"
)

const (
	// SignatureSchemeEd25519CommitmentV2 signs a compact, domain-separated
	// SHA-512 commitment to the canonical manifest. The signing message remains
	// below remote KMS RAW-message limits even when the manifest grows.
	SignatureSchemeEd25519CommitmentV2 = "ed25519-sha512-commitment-v2"

	manifestSignatureDomainV1  = "multica-role-source-disaster-recovery-manifest-v1\x00"
	manifestCommitmentDomainV2 = "multica-role-source-disaster-recovery-manifest-commitment-v2\x00"
	manifestSignatureDomainV2  = "multica-role-source-disaster-recovery-manifest-signature-v2\x00"
)

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
		if allowUnsigned && manifest.UnsignedDevelopment && manifest.Signature == "" && manifest.SignerKeyID == "" && manifest.SignatureScheme == "" {
			return nil
		}
		return errors.New("DR manifest signature is required")
	}
	if manifest.UnsignedDevelopment {
		return errors.New("signed DR manifest cannot be marked unsigned")
	}
	if !keyIDPattern.MatchString(manifest.SignerKeyID) {
		return errors.New("invalid DR manifest signer key id")
	}
	if manifest.SignatureScheme != "" && manifest.SignatureScheme != SignatureSchemeEd25519CommitmentV2 {
		return errors.New("unsupported DR manifest signature scheme")
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
	candidate := *manifest
	message, err := PrepareManifestSignature(&candidate, keyID)
	if err != nil {
		return err
	}
	publicKey, ok := privateKey.Public().(ed25519.PublicKey)
	if !ok {
		return errors.New("invalid Ed25519 DR signing private key")
	}
	if err := AttachManifestSignature(&candidate, ed25519.Sign(privateKey, message), publicKey); err != nil {
		return err
	}
	*manifest = candidate
	return nil
}

// PrepareManifestSignature binds the signer identity and current signature
// scheme to manifest before returning the exact compact message a local key or
// remote KMS must sign. The caller must not mutate manifest before attaching
// the returned signature.
func PrepareManifestSignature(manifest *Manifest, keyID string) ([]byte, error) {
	if manifest == nil || !keyIDPattern.MatchString(keyID) {
		return nil, errors.New("valid DR signer key id is required")
	}
	candidate := *manifest
	candidate.SignerKeyID = keyID
	candidate.SignatureScheme = SignatureSchemeEd25519CommitmentV2
	candidate.Signature = ""
	candidate.UnsignedDevelopment = false

	// Validate all non-cryptographic fields before spending a remote signing
	// operation. The placeholder is never returned or persisted.
	validationCopy := candidate
	validationCopy.Signature = base64.RawStdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
	if err := ValidateManifest(validationCopy, false); err != nil {
		return nil, err
	}
	message, err := manifestSignatureMessage(candidate)
	if err != nil {
		return nil, err
	}
	*manifest = candidate
	return message, nil
}

// AttachManifestSignature verifies a provider response locally before making
// it part of the manifest. A wrong KMS key, stale alias or corrupted response
// therefore fails closed before a backup is published.
func AttachManifestSignature(manifest *Manifest, signature []byte, publicKey ed25519.PublicKey) error {
	if manifest == nil || len(signature) != ed25519.SignatureSize || len(publicKey) != ed25519.PublicKeySize {
		return errors.New("valid Ed25519 DR signature and public key are required")
	}
	message, err := manifestSignatureMessage(*manifest)
	if err != nil {
		return err
	}
	if !ed25519.Verify(publicKey, message, signature) {
		return errors.New("DR signing provider returned an invalid signature")
	}
	candidate := *manifest
	candidate.Signature = base64.RawStdEncoding.EncodeToString(signature)
	if err := ValidateManifest(candidate, false); err != nil {
		return err
	}
	*manifest = candidate
	return nil
}

// GenerateSigningKey is a local/HSM bootstrap helper. Private bytes are
// returned only to the caller for immediate provisioning and are never
// persisted by the DR package. AWS KMS creates its own non-exportable key.
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
	message, err := manifestSignatureMessage(manifest)
	if err != nil {
		return err
	}
	if !ed25519.Verify(publicKey, message, signature) {
		return errors.New("DR manifest signature verification failed")
	}
	return nil
}

func manifestSignatureMessage(manifest Manifest) ([]byte, error) {
	manifest.Signature = ""
	body, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	if manifest.SignatureScheme == "" {
		return append([]byte(manifestSignatureDomainV1), body...), nil
	}
	if manifest.SignatureScheme != SignatureSchemeEd25519CommitmentV2 {
		return nil, errors.New("unsupported DR manifest signature scheme")
	}
	commitmentInput := make([]byte, 0, len(manifestCommitmentDomainV2)+len(body))
	commitmentInput = append(commitmentInput, manifestCommitmentDomainV2...)
	commitmentInput = append(commitmentInput, body...)
	commitment := sha512.Sum512(commitmentInput)
	message := make([]byte, 0, len(manifestSignatureDomainV2)+len(commitment))
	message = append(message, manifestSignatureDomainV2...)
	message = append(message, commitment[:]...)
	return message, nil
}
