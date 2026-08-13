package rolesourcedr

import (
	"crypto/sha256"
	"encoding/hex"
)

func digestBytes(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}
