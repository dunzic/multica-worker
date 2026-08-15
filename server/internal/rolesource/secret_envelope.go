package rolesource

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

const (
	SecretEnvelopeContractVersion = "1.0"
	secretEnvelopeKeyInfo         = "multica-role-source-secret-envelope-v1"
	maxSecretEnvelopePlaintext    = 256 << 10
	maxSecretEnvelopeValues       = 256
	maxSecretEnvelopeMCPServers   = 64
)

var (
	ErrInvalidSecretEnvelope = errors.New("invalid role source secret envelope")
	ErrExpiredSecretEnvelope = errors.New("role source secret envelope expired")
)

var environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,255}$`)

// SecretEnvelopeClaims are authenticated but not encrypted. They bind one
// transfer to the exact tenant, source, role and immutable snapshot approved
// by the control plane. ExpiresAt is RFC3339Nano UTC text for canonical JSON.
type SecretEnvelopeClaims struct {
	ContractVersion string `json:"contract_version"`
	TransferID      string `json:"transfer_id"`
	WorkspaceID     string `json:"workspace_id"`
	SourceID        string `json:"source_id"`
	RoleID          string `json:"role_id"`
	SnapshotDigest  string `json:"snapshot_digest"`
	ExpiresAt       string `json:"expires_at"`
}

// SecretEnvelopePayload never belongs in snapshots, plans, receipts, logs or
// ordinary APIs. Environment values and raw MCP definitions travel only in
// the authenticated ciphertext and are cleared by callers after consumption.
type SecretEnvelopePayload struct {
	Environment map[string]string          `json:"environment"`
	MCPServers  map[string]json.RawMessage `json:"mcp_servers"`
}

type SecretEnvelope struct {
	Claims             SecretEnvelopeClaims `json:"claims"`
	EphemeralPublicKey string               `json:"ephemeral_public_key"`
	Nonce              string               `json:"nonce"`
	Ciphertext         string               `json:"ciphertext"`
}

type SecretEnvelopeKeyPair struct {
	PrivateKey []byte
	PublicKey  string
}

func encodeSecretEnvelope(envelope SecretEnvelope) ([]byte, string, error) {
	body, err := json.Marshal(envelope)
	if err != nil {
		return nil, "", err
	}
	digestValue := sha256.Sum256(body)
	return body, "sha256:" + fmt.Sprintf("%x", digestValue[:]), nil
}

func NewSecretEnvelopeKeyPair() (SecretEnvelopeKeyPair, error) {
	privateKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return SecretEnvelopeKeyPair{}, fmt.Errorf("generate role source envelope key: %w", err)
	}
	return SecretEnvelopeKeyPair{
		PrivateKey: append([]byte(nil), privateKey.Bytes()...),
		PublicKey:  base64.RawURLEncoding.EncodeToString(privateKey.PublicKey().Bytes()),
	}, nil
}

func SealSecretEnvelope(serverPublicKey string, claims SecretEnvelopeClaims, payload SecretEnvelopePayload) (SecretEnvelope, error) {
	aad, err := canonicalSecretEnvelopeClaims(claims, time.Now().UTC(), true)
	if err != nil {
		return SecretEnvelope{}, err
	}
	plaintext, err := canonicalSecretEnvelopePayload(payload)
	if err != nil {
		return SecretEnvelope{}, err
	}
	defer clear(plaintext)
	if len(serverPublicKey) != base64.RawURLEncoding.EncodedLen(32) {
		return SecretEnvelope{}, fmt.Errorf("%w: invalid server public key", ErrInvalidSecretEnvelope)
	}
	serverPublicBytes, err := base64.RawURLEncoding.DecodeString(serverPublicKey)
	if err != nil {
		return SecretEnvelope{}, fmt.Errorf("%w: invalid server public key", ErrInvalidSecretEnvelope)
	}
	serverPublic, err := ecdh.X25519().NewPublicKey(serverPublicBytes)
	clear(serverPublicBytes)
	if err != nil {
		return SecretEnvelope{}, fmt.Errorf("%w: invalid server public key", ErrInvalidSecretEnvelope)
	}
	ephemeral, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return SecretEnvelope{}, err
	}
	shared, err := ephemeral.ECDH(serverPublic)
	if err != nil {
		return SecretEnvelope{}, fmt.Errorf("derive role source envelope key: %w", err)
	}
	key, err := deriveSecretEnvelopeKey(shared, aad)
	clear(shared)
	if err != nil {
		return SecretEnvelope{}, err
	}
	aead, err := secretEnvelopeAEAD(key)
	clear(key)
	if err != nil {
		return SecretEnvelope{}, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return SecretEnvelope{}, err
	}
	ciphertext := aead.Seal(nil, nonce, plaintext, aad)
	return SecretEnvelope{
		Claims: claims, EphemeralPublicKey: base64.RawURLEncoding.EncodeToString(ephemeral.PublicKey().Bytes()),
		Nonce: base64.RawURLEncoding.EncodeToString(nonce), Ciphertext: base64.RawURLEncoding.EncodeToString(ciphertext),
	}, nil
}

func OpenSecretEnvelope(serverPrivateKey []byte, envelope SecretEnvelope, now time.Time) (SecretEnvelopePayload, error) {
	aad, err := canonicalSecretEnvelopeClaims(envelope.Claims, now.UTC(), true)
	if err != nil {
		return SecretEnvelopePayload{}, err
	}
	if len(serverPrivateKey) != 32 {
		return SecretEnvelopePayload{}, fmt.Errorf("%w: invalid server private key", ErrInvalidSecretEnvelope)
	}
	privateKey, err := ecdh.X25519().NewPrivateKey(serverPrivateKey)
	if err != nil {
		return SecretEnvelopePayload{}, fmt.Errorf("%w: invalid server private key", ErrInvalidSecretEnvelope)
	}
	if len(envelope.EphemeralPublicKey) != base64.RawURLEncoding.EncodedLen(32) ||
		len(envelope.Nonce) != base64.RawURLEncoding.EncodedLen(12) ||
		len(envelope.Ciphertext) > base64.RawURLEncoding.EncodedLen(maxSecretEnvelopePlaintext+16) {
		return SecretEnvelopePayload{}, fmt.Errorf("%w: invalid encoded field length", ErrInvalidSecretEnvelope)
	}
	ephemeralBytes, err := base64.RawURLEncoding.DecodeString(envelope.EphemeralPublicKey)
	if err != nil {
		return SecretEnvelopePayload{}, fmt.Errorf("%w: invalid ephemeral public key", ErrInvalidSecretEnvelope)
	}
	ephemeralPublic, err := ecdh.X25519().NewPublicKey(ephemeralBytes)
	clear(ephemeralBytes)
	if err != nil {
		return SecretEnvelopePayload{}, fmt.Errorf("%w: invalid ephemeral public key", ErrInvalidSecretEnvelope)
	}
	shared, err := privateKey.ECDH(ephemeralPublic)
	if err != nil {
		return SecretEnvelopePayload{}, fmt.Errorf("%w: key agreement failed", ErrInvalidSecretEnvelope)
	}
	key, err := deriveSecretEnvelopeKey(shared, aad)
	clear(shared)
	if err != nil {
		return SecretEnvelopePayload{}, err
	}
	aead, err := secretEnvelopeAEAD(key)
	clear(key)
	if err != nil {
		return SecretEnvelopePayload{}, err
	}
	nonce, err := base64.RawURLEncoding.DecodeString(envelope.Nonce)
	if err != nil || len(nonce) != aead.NonceSize() {
		return SecretEnvelopePayload{}, fmt.Errorf("%w: invalid nonce", ErrInvalidSecretEnvelope)
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(envelope.Ciphertext)
	if err != nil || len(ciphertext) > maxSecretEnvelopePlaintext+aead.Overhead() {
		return SecretEnvelopePayload{}, fmt.Errorf("%w: invalid ciphertext", ErrInvalidSecretEnvelope)
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, aad)
	clear(ciphertext)
	if err != nil {
		return SecretEnvelopePayload{}, fmt.Errorf("%w: authentication failed", ErrInvalidSecretEnvelope)
	}
	defer clear(plaintext)
	var payload SecretEnvelopePayload
	decoder := json.NewDecoder(bytes.NewReader(plaintext))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return SecretEnvelopePayload{}, fmt.Errorf("%w: invalid plaintext", ErrInvalidSecretEnvelope)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return SecretEnvelopePayload{}, fmt.Errorf("%w: plaintext contains trailing JSON", ErrInvalidSecretEnvelope)
	}
	if _, err := canonicalSecretEnvelopePayload(payload); err != nil {
		return SecretEnvelopePayload{}, err
	}
	return payload, nil
}

// ClearSecretEnvelopePayload drops all reachable plaintext values as soon as a
// transfer is consumed. Go strings cannot provide a hard memory-erasure
// guarantee, so callers must also avoid logging/caching/copying the payload.
func ClearSecretEnvelopePayload(payload *SecretEnvelopePayload) {
	if payload == nil {
		return
	}
	for key := range payload.Environment {
		payload.Environment[key] = ""
		delete(payload.Environment, key)
	}
	for key, definition := range payload.MCPServers {
		clear(definition)
		delete(payload.MCPServers, key)
	}
}

func canonicalSecretEnvelopeClaims(claims SecretEnvelopeClaims, now time.Time, enforceExpiry bool) ([]byte, error) {
	if claims.ContractVersion != SecretEnvelopeContractVersion || !stableIDPattern.MatchString(claims.TransferID) ||
		!stableIDPattern.MatchString(claims.WorkspaceID) || !stableIDPattern.MatchString(claims.SourceID) ||
		!stableIDPattern.MatchString(claims.RoleID) || !sha256Pattern.MatchString(claims.SnapshotDigest) {
		return nil, fmt.Errorf("%w: invalid claims identity", ErrInvalidSecretEnvelope)
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, claims.ExpiresAt)
	if err != nil || expiresAt.Location() != time.UTC || expiresAt.Format(time.RFC3339Nano) != claims.ExpiresAt {
		return nil, fmt.Errorf("%w: expiry must be canonical UTC RFC3339Nano", ErrInvalidSecretEnvelope)
	}
	if enforceExpiry && !expiresAt.After(now) {
		return nil, ErrExpiredSecretEnvelope
	}
	if expiresAt.After(now.Add(15 * time.Minute)) {
		return nil, fmt.Errorf("%w: expiry exceeds 15 minute transfer window", ErrInvalidSecretEnvelope)
	}
	return json.Marshal(claims)
}

func canonicalSecretEnvelopePayload(payload SecretEnvelopePayload) ([]byte, error) {
	if payload.Environment == nil {
		payload.Environment = map[string]string{}
	}
	if payload.MCPServers == nil {
		payload.MCPServers = map[string]json.RawMessage{}
	}
	if len(payload.Environment) > maxSecretEnvelopeValues || len(payload.MCPServers) > maxSecretEnvelopeMCPServers {
		return nil, fmt.Errorf("%w: payload object count exceeds limits", ErrInvalidSecretEnvelope)
	}
	for name, value := range payload.Environment {
		if !environmentNamePattern.MatchString(name) || len(value) > 32<<10 || strings.IndexByte(value, 0) >= 0 {
			return nil, fmt.Errorf("%w: invalid environment value", ErrInvalidSecretEnvelope)
		}
	}
	for id, definition := range payload.MCPServers {
		if !stableIDPattern.MatchString(id) || len(definition) == 0 || len(definition) > 64<<10 || !json.Valid(definition) {
			return nil, fmt.Errorf("%w: invalid MCP definition", ErrInvalidSecretEnvelope)
		}
		var value any
		if err := json.Unmarshal(definition, &value); err != nil {
			return nil, fmt.Errorf("%w: invalid MCP definition", ErrInvalidSecretEnvelope)
		}
		canonical, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		payload.MCPServers[id] = canonical
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if len(body) > maxSecretEnvelopePlaintext {
		return nil, fmt.Errorf("%w: payload exceeds size limit", ErrInvalidSecretEnvelope)
	}
	return body, nil
}

func deriveSecretEnvelopeKey(sharedSecret, aad []byte) ([]byte, error) {
	salt := sha256.Sum256(aad)
	return hkdf.Key(sha256.New, sharedSecret, salt[:], secretEnvelopeKeyInfo, 32)
}

func secretEnvelopeAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
