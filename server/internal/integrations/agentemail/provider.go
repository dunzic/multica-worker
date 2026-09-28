package agentemail

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// FailureClass describes whether another provider call is safe. Only
// FailureDefiniteRetryable may be retried automatically.
type FailureClass string

const (
	FailureDefiniteRetryable FailureClass = "definite_retryable"
	FailureDefinitePermanent FailureClass = "definite_permanent"
	FailureAmbiguous         FailureClass = "ambiguous"
)

const (
	ErrorProviderNotConfigured = "provider_not_configured"
	ErrorInvalidRequest        = "invalid_request"
	ErrorAuthentication        = "authentication"
	ErrorRateLimited           = "rate_limited"
	ErrorProviderRejected      = "provider_rejected"
	ErrorProviderUnavailable   = "provider_unavailable"
	ErrorResponseUnknown       = "response_unknown"
)

// NotificationRequest is the provider-facing form of an already-authorized
// notification. Policy evaluation and recipient authorization happen before
// this boundary.
type NotificationRequest struct {
	To             []string
	Subject        string
	HTMLBody       string
	TextBody       string
	From           string
	IdempotencyKey string
}

// NotificationResult means the provider accepted the request. It does not
// claim that the message reached any recipient.
type NotificationResult struct {
	ProviderMessageID string
	Status            string
}

// EmailProvider is deliberately narrower than the verification/invitation
// EmailService. Agent notifications must use a provider with idempotency and
// explicit acceptance semantics; SMTP does not satisfy this contract.
type EmailProvider interface {
	Send(context.Context, NotificationRequest) (NotificationResult, error)
}

// DeliveryError contains only safe metadata. Its Error string never includes
// recipients, message content, credentials, or a provider response body.
type DeliveryError struct {
	Class      FailureClass
	Code       string
	RetryAfter time.Duration
	attempts   int
	cause      error
}

func (e *DeliveryError) Error() string {
	if e == nil {
		return "agent email delivery failed"
	}
	return fmt.Sprintf("agent email delivery %s (%s)", e.Class, e.Code)
}

// Attempts is the number of provider calls made by a retrying provider.
func (e *DeliveryError) Attempts() int {
	if e == nil {
		return 0
	}
	return e.attempts
}

// Cause is available for diagnostics that explicitly need error identity.
// Callers must not log it without applying their own redaction policy.
func (e *DeliveryError) Cause() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func deliveryError(class FailureClass, code string, retryAfter time.Duration, cause error) *DeliveryError {
	return &DeliveryError{Class: class, Code: code, RetryAfter: retryAfter, attempts: 1, cause: cause}
}

func asDeliveryError(err error) (*DeliveryError, bool) {
	var deliveryErr *DeliveryError
	if !errors.As(err, &deliveryErr) {
		return nil, false
	}
	return deliveryErr, true
}

func validateRequest(request NotificationRequest) error {
	idempotencyKey := request.IdempotencyKey
	if len(request.To) == 0 || strings.TrimSpace(request.From) == "" || strings.TrimSpace(request.Subject) == "" ||
		(strings.TrimSpace(request.HTMLBody) == "" && strings.TrimSpace(request.TextBody) == "") ||
		strings.TrimSpace(idempotencyKey) == "" || len(idempotencyKey) > 256 || strings.ContainsAny(idempotencyKey, "\r\n") {
		return deliveryError(FailureDefinitePermanent, ErrorInvalidRequest, 0, nil)
	}
	for _, recipient := range request.To {
		if strings.TrimSpace(recipient) == "" {
			return deliveryError(FailureDefinitePermanent, ErrorInvalidRequest, 0, nil)
		}
	}
	return nil
}
