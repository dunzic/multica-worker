package secretbox

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"testing"
)

func mustNewBox(t *testing.T) *Box {
	t.Helper()
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand: %v", err)
	}
	box, err := New(key)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return box
}

func TestRoundTrip(t *testing.T) {
	box := mustNewBox(t)
	plaintext := []byte("lark app_secret 12345")
	sealed, err := box.Seal(plaintext)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	opened, err := box.Open(sealed)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(opened, plaintext) {
		t.Fatalf("round trip mismatch: got %q want %q", opened, plaintext)
	}
}

func TestSealIsNonDeterministic(t *testing.T) {
	// Same plaintext + same box → different ciphertext on each Seal,
	// because the nonce is random. This prevents content-fingerprinting
	// (e.g. confirming that two installations share the same secret).
	box := mustNewBox(t)
	plaintext := []byte("repeat")
	a, _ := box.Seal(plaintext)
	b, _ := box.Seal(plaintext)
	if bytes.Equal(a, b) {
		t.Fatalf("expected non-deterministic Seal, got identical ciphertexts")
	}
}

func TestOpenRejectsTampered(t *testing.T) {
	box := mustNewBox(t)
	sealed, _ := box.Seal([]byte("important"))
	// Flip a bit in the ciphertext portion (skip nonce).
	tampered := append([]byte(nil), sealed...)
	tampered[len(tampered)-1] ^= 0x01
	if _, err := box.Open(tampered); err == nil {
		t.Fatalf("expected auth failure on tampered ciphertext")
	}
}

func TestOpenRejectsShort(t *testing.T) {
	box := mustNewBox(t)
	if _, err := box.Open([]byte("short")); err != ErrCiphertextTooShort {
		t.Fatalf("expected ErrCiphertextTooShort, got %v", err)
	}
}

func TestAdditionalDataBindsCiphertextContext(t *testing.T) {
	box := mustNewBox(t)
	sealed, err := box.SealWithAAD([]byte("source secret"), []byte("workspace-a/source-a"))
	if err != nil {
		t.Fatal(err)
	}
	opened, err := box.OpenWithAAD(sealed, []byte("workspace-a/source-a"))
	if err != nil || string(opened) != "source secret" {
		t.Fatalf("open with matching context = %q, %v", opened, err)
	}
	if _, err := box.OpenWithAAD(sealed, []byte("workspace-b/source-a")); err == nil {
		t.Fatal("ciphertext opened under a different tenant context")
	}
}

func TestNewRejectsBadKey(t *testing.T) {
	if _, err := New(make([]byte, 16)); err != ErrInvalidKey {
		t.Fatalf("expected ErrInvalidKey for 16-byte key, got %v", err)
	}
}

func TestLoadKey(t *testing.T) {
	const envVar = "TEST_SECRETBOX_KEY"
	t.Run("missing", func(t *testing.T) {
		t.Setenv(envVar, "")
		if _, err := LoadKey(envVar); err == nil {
			t.Fatal("expected error on missing env var")
		}
	})
	t.Run("bad base64", func(t *testing.T) {
		t.Setenv(envVar, "not!base64!")
		if _, err := LoadKey(envVar); err == nil {
			t.Fatal("expected error on invalid base64")
		}
	})
	t.Run("wrong length", func(t *testing.T) {
		t.Setenv(envVar, base64.StdEncoding.EncodeToString([]byte("too short")))
		if _, err := LoadKey(envVar); err == nil {
			t.Fatal("expected error on short key")
		}
	})
	t.Run("happy path", func(t *testing.T) {
		key := make([]byte, KeySize)
		_, _ = rand.Read(key)
		t.Setenv(envVar, base64.StdEncoding.EncodeToString(key))
		got, err := LoadKey(envVar)
		if err != nil {
			t.Fatalf("LoadKey: %v", err)
		}
		if !bytes.Equal(got, key) {
			t.Fatalf("LoadKey returned wrong bytes")
		}
	})
}
