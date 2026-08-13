package signedremote

import (
	"encoding/json"
	"testing"
)

func FuzzSignedRemoteConfigParser(f *testing.F) {
	f.Add([]byte(`{"bundle_url":"https://publisher.example/bundle.json","artifact_base_url":"https://publisher.example/artifacts/","issuer":"publisher.example","public_keys":{"primary":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}}`))
	f.Add([]byte(`{"bundle_url":"https://127.0.0.1/private","public_keys":{}}`))
	f.Fuzz(func(t *testing.T, body []byte) {
		if len(body) > maxConfigBytes+1 {
			return
		}
		_, _ = decodeConfig(json.RawMessage(body))
	})
}

func FuzzSignedRemoteBundleParser(f *testing.F) {
	f.Add([]byte(`{"version":1,"issuer":"publisher.example","key_id":"primary","revision":"r1","manifest_digest":"sha256:00","tree_digest":"sha256:00","manifest":{"contract_version":"1.0","roles":[],"capabilities":[]},"signature":"AA=="}`))
	f.Add([]byte(`{"version":`))
	f.Fuzz(func(t *testing.T, body []byte) {
		if len(body) > maxBundleBytes+1 {
			return
		}
		_, _ = decodeBundle(body)
	})
}
