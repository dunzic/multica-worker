package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/endpointcreds"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/multica-ai/multica/server/internal/rolesourcedr"
)

const testKMSResolvedKeyID = "arn:aws:kms:us-east-1:111122223333:key/00000000-0000-4000-8000-000000000001"

type fakeAWSKMSClient struct {
	getPublicKey func(context.Context, *kms.GetPublicKeyInput) (*kms.GetPublicKeyOutput, error)
	sign         func(context.Context, *kms.SignInput) (*kms.SignOutput, error)
}

func (f *fakeAWSKMSClient) GetPublicKey(ctx context.Context, input *kms.GetPublicKeyInput, _ ...func(*kms.Options)) (*kms.GetPublicKeyOutput, error) {
	return f.getPublicKey(ctx, input)
}

func (f *fakeAWSKMSClient) Sign(ctx context.Context, input *kms.SignInput, _ ...func(*kms.Options)) (*kms.SignOutput, error) {
	return f.sign(ctx, input)
}

func TestAWSKMSSigningPinsKeyAndVerifiesResponseLocally(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	client := validFakeAWSKMSClient(t, publicKey, privateKey)
	manifest := emptyValidManifest(time.Now())
	if err := signManifestWithAWSKMS(context.Background(), &manifest, "backup-kms-v1", "alias/multica-dr", publicKey, client); err != nil {
		t.Fatal(err)
	}
	if manifest.SignatureScheme != rolesourcedr.SignatureSchemeEd25519CommitmentV2 {
		t.Fatalf("signature scheme = %q", manifest.SignatureScheme)
	}
	if err := rolesourcedr.VerifyManifestSignature(manifest, map[string]ed25519.PublicKey{"backup-kms-v1": publicKey}, false); err != nil {
		t.Fatal(err)
	}
}

func TestAWSKMSSigningForcesOfficialServiceEndpoint(t *testing.T) {
	options := kms.Options{BaseEndpoint: stringPointer("https://untrusted-storage-endpoint.example")}
	forceOfficialAWSKMSEndpoint(&options)
	if options.BaseEndpoint != nil {
		t.Fatalf("custom KMS endpoint remained configured: %q", stringValue(options.BaseEndpoint))
	}
	if options.EndpointResolverV2 == nil {
		t.Fatal("official KMS endpoint resolver was not installed")
	}
}

func TestAWSKMSConfigDisablesSharedConfigurationFiles(t *testing.T) {
	options := awsconfig.LoadOptions{}
	for _, apply := range awsKMSConfigLoadOptions() {
		if err := apply(&options); err != nil {
			t.Fatal(err)
		}
	}
	if options.SharedConfigFiles == nil || len(options.SharedConfigFiles) != 0 {
		t.Fatalf("shared config files = %#v, want an explicit empty list", options.SharedConfigFiles)
	}
	if options.SharedCredentialsFiles == nil || len(options.SharedCredentialsFiles) != 0 {
		t.Fatalf("shared credentials files = %#v, want an explicit empty list", options.SharedCredentialsFiles)
	}
}

func TestAWSKMSConfigIgnoresDefaultSharedConfigFile(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(configPath, []byte("[default]\nregion=us-west-1\naws_access_key_id=shared-access\naws_secret_access_key=shared-secret\nendpoint_url=https://untrusted.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", configPath)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "")
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_WEB_IDENTITY_TOKEN_FILE", "")
	t.Setenv("AWS_ROLE_ARN", "")
	t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", "")
	t.Setenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")

	config, err := awsconfig.LoadDefaultConfig(context.Background(), awsKMSConfigLoadOptions()...)
	if err != nil {
		t.Fatal(err)
	}
	if config.Region != "us-east-1" {
		t.Fatalf("shared config changed region to %q", config.Region)
	}
	if credentials, err := config.Credentials.Retrieve(context.Background()); err == nil {
		t.Fatalf("shared config credentials were loaded: source=%q", credentials.Source)
	}
}

func TestAWSKMSSigningAcceptsOnlyWorkloadIdentityCredentialSources(t *testing.T) {
	for _, source := range []string{stscreds.WebIdentityProviderName, endpointcreds.ProviderName} {
		if !isWorkloadIdentityCredentialSource(source) {
			t.Fatalf("workload identity source %q rejected", source)
		}
	}
	for _, source := range []string{"", "EnvConfigCredentials", "SharedConfigCredentials: /private/credentials", "StaticCredentials", "EC2RoleProvider", "AssumeRoleProvider"} {
		if isWorkloadIdentityCredentialSource(source) {
			t.Fatalf("non-workload credential source %q accepted", source)
		}
	}
}

func TestAWSKMSSigningRejectsMetadataAndPinDrift(t *testing.T) {
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherPublicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	validDER, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		mutate func(*kms.GetPublicKeyOutput)
	}{
		{name: "wrong key spec", mutate: func(output *kms.GetPublicKeyOutput) { output.KeySpec = types.KeySpecEccNistP256 }},
		{name: "wrong key usage", mutate: func(output *kms.GetPublicKeyOutput) { output.KeyUsage = types.KeyUsageTypeEncryptDecrypt }},
		{name: "missing signing algorithm", mutate: func(output *kms.GetPublicKeyOutput) { output.SigningAlgorithms = nil }},
		{name: "missing resolved key id", mutate: func(output *kms.GetPublicKeyOutput) { output.KeyId = nil }},
		{name: "invalid DER", mutate: func(output *kms.GetPublicKeyOutput) { output.PublicKey = []byte("not-der") }},
		{name: "alias drift to another key", mutate: func(output *kms.GetPublicKeyOutput) {
			output.PublicKey, _ = x509.MarshalPKIXPublicKey(otherPublicKey)
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			output := validKMSPublicKeyOutput(validDER)
			testCase.mutate(output)
			client := &fakeAWSKMSClient{
				getPublicKey: func(context.Context, *kms.GetPublicKeyInput) (*kms.GetPublicKeyOutput, error) { return output, nil },
				sign: func(context.Context, *kms.SignInput) (*kms.SignOutput, error) {
					t.Fatal("Sign called after invalid KMS metadata")
					return nil, nil
				},
			}
			manifest := emptyValidManifest(time.Now())
			original := manifest
			if err := signManifestWithAWSKMS(context.Background(), &manifest, "backup-kms-v1", "alias/multica-dr", publicKey, client); err == nil {
				t.Fatal("invalid KMS key accepted")
			}
			if manifest.Signature != original.Signature || manifest.SignerKeyID != original.SignerKeyID || manifest.SignatureScheme != original.SignatureScheme {
				t.Fatal("failed signing attempt mutated published manifest")
			}
		})
	}
}

func TestAWSKMSSigningRejectsFailedOrTamperedResponsesWithoutLeakingProviderDetails(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	sensitiveDetail := "DisabledException arn:aws:kms:us-east-1:999999999999:key/private-customer-key"

	cases := []struct {
		name string
		get  func(context.Context, *kms.GetPublicKeyInput) (*kms.GetPublicKeyOutput, error)
		sign func(context.Context, *kms.SignInput) (*kms.SignOutput, error)
	}{
		{
			name: "disabled or revoked key",
			get: func(context.Context, *kms.GetPublicKeyInput) (*kms.GetPublicKeyOutput, error) {
				return nil, errors.New(sensitiveDetail)
			},
			sign: func(context.Context, *kms.SignInput) (*kms.SignOutput, error) { return nil, nil },
		},
		{
			name: "sign service failure",
			get: func(context.Context, *kms.GetPublicKeyInput) (*kms.GetPublicKeyOutput, error) {
				return validKMSPublicKeyOutput(der), nil
			},
			sign: func(context.Context, *kms.SignInput) (*kms.SignOutput, error) {
				return nil, errors.New(sensitiveDetail)
			},
		},
		{
			name: "wrong response key",
			get: func(context.Context, *kms.GetPublicKeyInput) (*kms.GetPublicKeyOutput, error) {
				return validKMSPublicKeyOutput(der), nil
			},
			sign: func(_ context.Context, input *kms.SignInput) (*kms.SignOutput, error) {
				return &kms.SignOutput{KeyId: stringPointer(testKMSResolvedKeyID + "-wrong"), SigningAlgorithm: input.SigningAlgorithm, Signature: ed25519.Sign(privateKey, input.Message)}, nil
			},
		},
		{
			name: "wrong response algorithm",
			get: func(context.Context, *kms.GetPublicKeyInput) (*kms.GetPublicKeyOutput, error) {
				return validKMSPublicKeyOutput(der), nil
			},
			sign: func(_ context.Context, input *kms.SignInput) (*kms.SignOutput, error) {
				return &kms.SignOutput{KeyId: stringPointer(testKMSResolvedKeyID), SigningAlgorithm: types.SigningAlgorithmSpecEcdsaSha256, Signature: ed25519.Sign(privateKey, input.Message)}, nil
			},
		},
		{
			name: "tampered signature",
			get: func(context.Context, *kms.GetPublicKeyInput) (*kms.GetPublicKeyOutput, error) {
				return validKMSPublicKeyOutput(der), nil
			},
			sign: func(_ context.Context, input *kms.SignInput) (*kms.SignOutput, error) {
				signature := ed25519.Sign(privateKey, input.Message)
				signature[0] ^= 0xff
				return &kms.SignOutput{KeyId: stringPointer(testKMSResolvedKeyID), SigningAlgorithm: input.SigningAlgorithm, Signature: signature}, nil
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			manifest := emptyValidManifest(time.Now())
			original := manifest
			err := signManifestWithAWSKMS(context.Background(), &manifest, "backup-kms-v1", "alias/multica-dr", publicKey, &fakeAWSKMSClient{getPublicKey: testCase.get, sign: testCase.sign})
			if err == nil {
				t.Fatal("failed or tampered KMS response accepted")
			}
			if strings.Contains(err.Error(), "999999999999") || strings.Contains(err.Error(), "private-customer-key") || strings.Contains(err.Error(), "DisabledException") {
				t.Fatalf("provider detail leaked: %v", err)
			}
			if manifest.Signature != original.Signature || manifest.SignerKeyID != original.SignerKeyID || manifest.SignatureScheme != original.SignatureScheme {
				t.Fatal("failed signing response mutated published manifest")
			}
		})
	}
}

func TestAWSKMSSigningHonorsCancellationWithoutMutatingManifest(t *testing.T) {
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &fakeAWSKMSClient{
		getPublicKey: func(ctx context.Context, _ *kms.GetPublicKeyInput) (*kms.GetPublicKeyOutput, error) {
			return nil, ctx.Err()
		},
		sign: func(context.Context, *kms.SignInput) (*kms.SignOutput, error) {
			t.Fatal("Sign called after cancellation")
			return nil, nil
		},
	}
	manifest := emptyValidManifest(time.Now())
	original := manifest
	if err := signManifestWithAWSKMS(ctx, &manifest, "backup-kms-v1", "alias/multica-dr", publicKey, client); err == nil {
		t.Fatal("cancelled KMS request accepted")
	}
	if manifest.Signature != original.Signature || manifest.SignerKeyID != original.SignerKeyID || manifest.SignatureScheme != original.SignatureScheme {
		t.Fatal("cancelled signing attempt mutated published manifest")
	}
}

func TestSigningProviderConfigurationFailsClosedAndSupportsKMS(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	setSigningEnvironment := func(provider, signerKeyID, private, kmsKeyID, pinned string) {
		t.Setenv("MULTICA_ROLE_SOURCE_DR_SIGNING_PROVIDER", provider)
		t.Setenv("MULTICA_ROLE_SOURCE_DR_SIGNING_KEY_ID", signerKeyID)
		t.Setenv("MULTICA_ROLE_SOURCE_DR_SIGNING_PRIVATE_KEY", private)
		t.Setenv("MULTICA_ROLE_SOURCE_DR_AWS_KMS_KEY_ID", kmsKeyID)
		t.Setenv("MULTICA_ROLE_SOURCE_DR_SIGNING_PUBLIC_KEY", pinned)
	}
	for _, name := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_SHARED_CREDENTIALS_FILE", "AWS_PROFILE", "AWS_ENDPOINT_URL", "AWS_ENDPOINT_URL_KMS", "AWS_ENDPOINT_URL_STS", "AWS_ENDPOINT_URL_S3"} {
		t.Setenv(name, "")
	}

	setSigningEnvironment("aws_kms", "backup-kms-v1", base64.StdEncoding.EncodeToString(privateKey), "alias/multica-dr", base64.StdEncoding.EncodeToString(publicKey))
	if err := signBackupManifestWithKMSLoader(context.Background(), manifestPointer(), false, func(context.Context) (awsKMSSigningClient, error) {
		t.Fatal("KMS loader called with raw private key configured")
		return nil, nil
	}); err == nil {
		t.Fatal("AWS KMS provider accepted a raw private key")
	}

	setSigningEnvironment("aws_kms", "backup-kms-v1", "", "alias/multica-dr", base64.StdEncoding.EncodeToString(publicKey))
	t.Setenv("AWS_ACCESS_KEY_ID", "static-access-key-must-not-be-used")
	if err := signBackupManifestWithKMSLoader(context.Background(), manifestPointer(), false, func(context.Context) (awsKMSSigningClient, error) {
		t.Fatal("KMS loader called with static AWS credentials configured")
		return nil, nil
	}); err == nil || !strings.Contains(err.Error(), "workload identity") {
		t.Fatalf("AWS KMS provider accepted static credentials: %v", err)
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "")

	for _, name := range []string{"AWS_ENDPOINT_URL", "AWS_ENDPOINT_URL_KMS", "AWS_ENDPOINT_URL_STS", "AWS_ENDPOINT_URL_S3"} {
		t.Setenv(name, "https://untrusted-endpoint.example")
		if err := signBackupManifestWithKMSLoader(context.Background(), manifestPointer(), false, func(context.Context) (awsKMSSigningClient, error) {
			t.Fatalf("KMS loader called with %s configured", name)
			return nil, nil
		}); err == nil || !strings.Contains(err.Error(), "endpoint overrides") {
			t.Fatalf("AWS KMS provider accepted %s: %v", name, err)
		}
		t.Setenv(name, "")
	}
	t.Setenv("S3_ENDPOINT_URL", "https://objects.example.com")
	if err := validateAWSKMSRuntimeEnvironment(); err != nil {
		t.Fatalf("service-specific S3 endpoint was rejected: %v", err)
	}

	setSigningEnvironment("aws_kms", "backup-kms-v1", "", "alias/multica-dr", base64.StdEncoding.EncodeToString(publicKey))
	loaderError := signBackupManifestWithKMSLoader(context.Background(), manifestPointer(), false, func(context.Context) (awsKMSSigningClient, error) {
		return nil, errors.New("credential chain exposed arn:aws:iam::999999999999:role/private-role")
	})
	if loaderError == nil || strings.Contains(loaderError.Error(), "999999999999") || strings.Contains(loaderError.Error(), "private-role") {
		t.Fatalf("KMS loader error was not safely reduced: %v", loaderError)
	}

	manifest := manifestPointer()
	if err := signBackupManifestWithKMSLoader(context.Background(), manifest, false, func(context.Context) (awsKMSSigningClient, error) {
		return validFakeAWSKMSClient(t, publicKey, privateKey), nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := rolesourcedr.VerifyManifestSignature(*manifest, map[string]ed25519.PublicKey{"backup-kms-v1": publicKey}, false); err != nil {
		t.Fatal(err)
	}

	setSigningEnvironment("future-provider", "backup-kms-v1", "", "", "")
	if err := signBackupManifestWithKMSLoader(context.Background(), manifestPointer(), false, nil); err == nil {
		t.Fatal("unknown signing provider accepted")
	}

	setSigningEnvironment("private_key", "backup-v1", base64.StdEncoding.EncodeToString(privateKey), "alias/stale", "")
	if err := signBackupManifestWithKMSLoader(context.Background(), manifestPointer(), false, nil); err == nil {
		t.Fatal("ambiguous private-key and KMS configuration accepted")
	}
}

func TestRunBackupRejectsInvalidSigningConfigurationBeforeCreatingOutput(t *testing.T) {
	tests := []struct {
		name      string
		provider  string
		configure func(*testing.T)
		wantError string
	}{
		{
			name:     "KMS endpoint override",
			provider: "aws_kms",
			configure: func(t *testing.T) {
				t.Setenv("AWS_ENDPOINT_URL_STS", "https://untrusted-sts.example")
			},
			wantError: "endpoint overrides",
		},
		{name: "missing KMS pin", provider: "aws_kms", wantError: "requires signer key id"},
		{name: "unknown provider", provider: "future_provider", wantError: "unsupported DR signing provider"},
		{
			name:     "invalid private key",
			provider: "private_key",
			configure: func(t *testing.T) {
				t.Setenv("MULTICA_ROLE_SOURCE_DR_SIGNING_KEY_ID", "backup-v1")
				t.Setenv("MULTICA_ROLE_SOURCE_DR_SIGNING_PRIVATE_KEY", "not-base64")
			},
			wantError: "invalid base64",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MULTICA_ROLE_SOURCE_DR_SIGNING_PROVIDER", tc.provider)
			for _, name := range []string{"MULTICA_ROLE_SOURCE_DR_SIGNING_KEY_ID", "MULTICA_ROLE_SOURCE_DR_SIGNING_PRIVATE_KEY", "MULTICA_ROLE_SOURCE_DR_AWS_KMS_KEY_ID", "MULTICA_ROLE_SOURCE_DR_SIGNING_PUBLIC_KEY", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_SHARED_CREDENTIALS_FILE", "AWS_PROFILE", "AWS_ENDPOINT_URL", "AWS_ENDPOINT_URL_KMS", "AWS_ENDPOINT_URL_STS", "AWS_ENDPOINT_URL_S3"} {
				t.Setenv(name, "")
			}
			if tc.configure != nil {
				tc.configure(t)
			}
			output := filepath.Join(t.TempDir(), "must-not-exist")
			err := runBackup(context.Background(), []string{"--output-dir", output})
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("runBackup() error = %v, want substring %q", err, tc.wantError)
			}
			if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
				t.Fatalf("backup output was created before signing configuration rejection: %v", statErr)
			}
		})
	}
}

func validFakeAWSKMSClient(t *testing.T, publicKey ed25519.PublicKey, privateKey ed25519.PrivateKey) *fakeAWSKMSClient {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	return &fakeAWSKMSClient{
		getPublicKey: func(ctx context.Context, input *kms.GetPublicKeyInput) (*kms.GetPublicKeyOutput, error) {
			assertKMSDeadline(t, ctx)
			if stringValue(input.KeyId) != "alias/multica-dr" {
				t.Fatalf("GetPublicKey key id = %q", stringValue(input.KeyId))
			}
			return validKMSPublicKeyOutput(der), nil
		},
		sign: func(ctx context.Context, input *kms.SignInput) (*kms.SignOutput, error) {
			assertKMSDeadline(t, ctx)
			if stringValue(input.KeyId) != testKMSResolvedKeyID {
				t.Fatalf("Sign did not pin resolved key id: %q", stringValue(input.KeyId))
			}
			if input.MessageType != types.MessageTypeRaw || input.SigningAlgorithm != types.SigningAlgorithmSpecEd25519Sha512 {
				t.Fatalf("Sign contract = %q/%q", input.MessageType, input.SigningAlgorithm)
			}
			if len(input.Message) == 0 || len(input.Message) > 4096 {
				t.Fatalf("Sign message size = %d", len(input.Message))
			}
			return &kms.SignOutput{
				KeyId:            stringPointer(testKMSResolvedKeyID),
				SigningAlgorithm: types.SigningAlgorithmSpecEd25519Sha512,
				Signature:        ed25519.Sign(privateKey, input.Message),
			}, nil
		},
	}
}

func assertKMSDeadline(t *testing.T, ctx context.Context) {
	t.Helper()
	deadline, ok := ctx.Deadline()
	remaining := time.Until(deadline)
	if !ok || remaining <= 0 || remaining > awsKMSOperationTimeout {
		t.Fatalf("KMS context deadline remaining = %v, present = %v", remaining, ok)
	}
}

func validKMSPublicKeyOutput(der []byte) *kms.GetPublicKeyOutput {
	return &kms.GetPublicKeyOutput{
		KeyId:             stringPointer(testKMSResolvedKeyID),
		KeySpec:           types.KeySpecEccNistEdwards25519,
		KeyUsage:          types.KeyUsageTypeSignVerify,
		PublicKey:         der,
		SigningAlgorithms: []types.SigningAlgorithmSpec{types.SigningAlgorithmSpecEd25519Sha512},
	}
}

func manifestPointer() *rolesourcedr.Manifest {
	manifest := emptyValidManifest(time.Now())
	return &manifest
}
