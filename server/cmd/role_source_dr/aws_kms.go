package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"errors"
	"strings"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/endpointcreds"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/multica-ai/multica/server/internal/rolesourcedr"
)

const awsKMSOperationTimeout = 15 * time.Second

type awsKMSSigningClient interface {
	GetPublicKey(context.Context, *kms.GetPublicKeyInput, ...func(*kms.Options)) (*kms.GetPublicKeyOutput, error)
	Sign(context.Context, *kms.SignInput, ...func(*kms.Options)) (*kms.SignOutput, error)
}

type awsKMSClientLoader func(context.Context) (awsKMSSigningClient, error)

func loadDefaultAWSKMSClient(ctx context.Context) (awsKMSSigningClient, error) {
	config, err := awsconfig.LoadDefaultConfig(ctx, awsKMSConfigLoadOptions()...)
	if err != nil {
		return nil, errors.New("AWS KMS signing client is unavailable")
	}
	credentialContext, cancelCredentials := context.WithTimeout(ctx, awsKMSOperationTimeout)
	credentials, err := config.Credentials.Retrieve(credentialContext)
	cancelCredentials()
	if err != nil || !isWorkloadIdentityCredentialSource(credentials.Source) {
		return nil, errors.New("AWS KMS signing client is unavailable")
	}
	return kms.NewFromConfig(config, forceOfficialAWSKMSEndpoint), nil
}

func awsKMSConfigLoadOptions() []func(*awsconfig.LoadOptions) error {
	return []func(*awsconfig.LoadOptions) error{
		awsconfig.WithSharedConfigFiles([]string{}),
		awsconfig.WithSharedCredentialsFiles([]string{}),
	}
}

func isWorkloadIdentityCredentialSource(source string) bool {
	return source == stscreds.WebIdentityProviderName || source == endpointcreds.ProviderName
}

// forceOfficialAWSKMSEndpoint is defense in depth after the runtime rejects
// global and service-specific endpoint overrides. KMS endpoint selection still
// honors the SDK's region and FIPS controls.
func forceOfficialAWSKMSEndpoint(options *kms.Options) {
	options.BaseEndpoint = nil
	options.EndpointResolverV2 = kms.NewDefaultEndpointResolverV2()
}

func signManifestWithAWSKMS(ctx context.Context, manifest *rolesourcedr.Manifest, signerKeyID, kmsKeyID string, pinnedPublicKey ed25519.PublicKey, client awsKMSSigningClient) error {
	if manifest == nil || client == nil || strings.TrimSpace(kmsKeyID) == "" || len(pinnedPublicKey) != ed25519.PublicKeySize {
		return errors.New("complete AWS KMS DR signing configuration is required")
	}

	candidate := *manifest
	message, err := rolesourcedr.PrepareManifestSignature(&candidate, signerKeyID)
	if err != nil {
		return err
	}
	if len(message) > 4096 {
		return errors.New("DR signing message exceeds the AWS KMS RAW-message limit")
	}

	metadataContext, cancelMetadata := context.WithTimeout(ctx, awsKMSOperationTimeout)
	publicResult, err := client.GetPublicKey(metadataContext, &kms.GetPublicKeyInput{KeyId: stringPointer(kmsKeyID)})
	cancelMetadata()
	if err != nil || publicResult == nil {
		return errors.New("AWS KMS signing key is unavailable")
	}
	resolvedKeyID := strings.TrimSpace(stringValue(publicResult.KeyId))
	if resolvedKeyID == "" || publicResult.KeySpec != types.KeySpecEccNistEdwards25519 || publicResult.KeyUsage != types.KeyUsageTypeSignVerify ||
		!containsSigningAlgorithm(publicResult.SigningAlgorithms, types.SigningAlgorithmSpecEd25519Sha512) {
		return errors.New("AWS KMS signing key metadata is incompatible")
	}
	parsedPublicKey, err := x509.ParsePKIXPublicKey(publicResult.PublicKey)
	if err != nil {
		return errors.New("AWS KMS signing key public material is invalid")
	}
	kmsPublicKey, ok := parsedPublicKey.(ed25519.PublicKey)
	if !ok || !bytes.Equal(kmsPublicKey, pinnedPublicKey) {
		return errors.New("AWS KMS signing key does not match the pinned public key")
	}

	signContext, cancelSign := context.WithTimeout(ctx, awsKMSOperationTimeout)
	signResult, err := client.Sign(signContext, &kms.SignInput{
		KeyId:            stringPointer(resolvedKeyID),
		Message:          message,
		MessageType:      types.MessageTypeRaw,
		SigningAlgorithm: types.SigningAlgorithmSpecEd25519Sha512,
	})
	cancelSign()
	if err != nil || signResult == nil {
		return errors.New("AWS KMS signing operation failed")
	}
	if stringValue(signResult.KeyId) != resolvedKeyID || signResult.SigningAlgorithm != types.SigningAlgorithmSpecEd25519Sha512 {
		return errors.New("AWS KMS signing response metadata is inconsistent")
	}
	if err := rolesourcedr.AttachManifestSignature(&candidate, signResult.Signature, pinnedPublicKey); err != nil {
		return errors.New("AWS KMS signing response failed local verification")
	}
	*manifest = candidate
	return nil
}

func containsSigningAlgorithm(values []types.SigningAlgorithmSpec, expected types.SigningAlgorithmSpec) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func stringPointer(value string) *string { return &value }

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
