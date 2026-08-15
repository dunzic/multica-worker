package rolesource

import "errors"

const (
	ScanFailureRemoteUnavailable  = "remote_unavailable"
	ScanFailureRemoteTrustInvalid = "remote_trust_invalid"
	ScanFailureRemoteContent      = "remote_content_invalid"
)

type scanFailure struct {
	code string
	err  error
}

func (e *scanFailure) Error() string { return e.err.Error() }
func (e *scanFailure) Unwrap() error { return e.err }

// NewScanFailure attaches one closed, content-free daemon result code while
// preserving error identity for local classification.
func NewScanFailure(code string, err error) error {
	if err == nil {
		return nil
	}
	switch code {
	case ScanFailureRemoteUnavailable, ScanFailureRemoteTrustInvalid, ScanFailureRemoteContent:
		return &scanFailure{code: code, err: err}
	default:
		return err
	}
}

func ScanFailureCode(err error) (string, bool) {
	var failure *scanFailure
	if !errors.As(err, &failure) {
		return "", false
	}
	return failure.code, true
}
