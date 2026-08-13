package rolesource

import (
	"errors"
	"testing"
)

func TestScanFailureCodesAreClosedAndPreserveCause(t *testing.T) {
	cause := errors.New("local detail")
	wrapped := NewScanFailure(ScanFailureRemoteTrustInvalid, cause)
	if code, ok := ScanFailureCode(wrapped); !ok || code != ScanFailureRemoteTrustInvalid || !errors.Is(wrapped, cause) {
		t.Fatalf("coded failure code=%q ok=%t err=%v", code, ok, wrapped)
	}
	unknown := NewScanFailure("tenant-controlled-code", cause)
	if _, ok := ScanFailureCode(unknown); ok || unknown != cause {
		t.Fatalf("unknown code escaped closed taxonomy: %v", unknown)
	}
}
