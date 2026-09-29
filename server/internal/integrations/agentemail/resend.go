package agentemail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	resendAPIURL       = "https://api.resend.com/emails"
	maxErrorBodyBytes  = 64 << 10
	providerStatusSent = "accepted"
	// ResendAPIKeyEnv is intentionally distinct from RESEND_API_KEY, which is
	// used by the verification and invitation EmailService.
	ResendAPIKeyEnv = "MULTICA_AGENT_EMAIL_RESEND_API_KEY"
)

// Clock is injectable so Retry-After HTTP dates and retry budgets are
// deterministic in tests.
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now().UTC() }

// ResendProvider is the production HTTP adapter for Agent notification mail.
// It never logs request content or credentials.
type ResendProvider struct {
	client *http.Client
	apiKey string
	url    string
	clock  Clock
}

// NewResendProviderFromEnv loads the server-owned notification credential.
// Missing credentials fail closed before any provider can be constructed.
func NewResendProviderFromEnv(client *http.Client) (*ResendProvider, error) {
	apiKey := strings.TrimSpace(os.Getenv(ResendAPIKeyEnv))
	if apiKey == "" {
		return nil, deliveryError(FailureDefinitePermanent, ErrorProviderNotConfigured, 0, nil)
	}
	return newResendProvider(client, apiKey, resendAPIURL, realClock{})
}

func newResendProvider(client *http.Client, apiKey, endpoint string, clock Clock) (*ResendProvider, error) {
	if strings.TrimSpace(apiKey) == "" || strings.TrimSpace(endpoint) == "" {
		return nil, deliveryError(FailureDefinitePermanent, ErrorProviderNotConfigured, 0, nil)
	}
	if client == nil {
		client = &http.Client{Timeout: time.Minute}
	} else if client.Timeout <= 0 {
		clone := *client
		clone.Timeout = time.Minute
		client = &clone
	}
	if clock == nil {
		clock = realClock{}
	}
	return &ResendProvider{client: client, apiKey: apiKey, url: endpoint, clock: clock}, nil
}

type resendSendRequest struct {
	From    string   `json:"from"`
	To      []string `json:"to"`
	Subject string   `json:"subject"`
	HTML    string   `json:"html,omitempty"`
	Text    string   `json:"text,omitempty"`
}

type resendSendResponse struct {
	ID string `json:"id"`
}

func (p *ResendProvider) Send(ctx context.Context, request NotificationRequest) (NotificationResult, error) {
	if p == nil || p.client == nil || strings.TrimSpace(p.apiKey) == "" {
		return NotificationResult{}, deliveryError(FailureDefinitePermanent, ErrorProviderNotConfigured, 0, nil)
	}
	if err := validateRequest(request); err != nil {
		return NotificationResult{}, err
	}

	payload, err := json.Marshal(resendSendRequest{
		From: request.From, To: request.To, Subject: request.Subject,
		HTML: request.HTMLBody, Text: request.TextBody,
	})
	if err != nil {
		return NotificationResult{}, deliveryError(FailureDefinitePermanent, ErrorInvalidRequest, 0, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(payload))
	if err != nil {
		return NotificationResult{}, deliveryError(FailureDefinitePermanent, ErrorInvalidRequest, 0, err)
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Idempotency-Key", request.IdempotencyKey)

	response, err := p.client.Do(req)
	if err != nil {
		// net/http cannot prove whether the peer accepted a request before a
		// transport error. Retrying here could duplicate an email.
		return NotificationResult{}, deliveryError(FailureAmbiguous, ErrorResponseUnknown, 0, err)
	}
	defer response.Body.Close()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxErrorBodyBytes))
		return NotificationResult{}, classifyHTTPFailure(response.StatusCode, response.Header, p.clock.Now())
	}

	var providerResponse resendSendResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, maxErrorBodyBytes)).Decode(&providerResponse); err != nil {
		return NotificationResult{}, deliveryError(FailureAmbiguous, ErrorResponseUnknown, 0, err)
	}
	providerResponse.ID = strings.TrimSpace(providerResponse.ID)
	if providerResponse.ID == "" {
		return NotificationResult{}, deliveryError(FailureAmbiguous, ErrorResponseUnknown, 0, errors.New("provider returned an empty message id"))
	}
	return NotificationResult{ProviderMessageID: providerResponse.ID, Status: providerStatusSent}, nil
}

func classifyHTTPFailure(status int, header http.Header, now time.Time) error {
	retryAfter := parseRetryAfter(header.Get("Retry-After"), now)
	switch {
	case status == http.StatusTooManyRequests:
		return deliveryError(FailureDefiniteRetryable, ErrorRateLimited, retryAfter, nil)
	case status >= http.StatusInternalServerError:
		// Resend retains POST /emails idempotency keys for 24 hours and
		// guarantees that replaying the same request is safe. Keep the durable
		// worker retry horizon below that provider window.
		return deliveryError(FailureDefiniteRetryable, ErrorProviderUnavailable, retryAfter, nil)
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return deliveryError(FailureDefinitePermanent, ErrorAuthentication, 0, nil)
	default:
		return deliveryError(FailureDefinitePermanent, ErrorProviderRejected, 0, nil)
	}
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds <= 0 {
			return 0
		}
		if seconds > int64((time.Duration(1<<63-1))/time.Second) {
			return time.Duration(1<<63 - 1)
		}
		return time.Duration(seconds) * time.Second
	}
	when, err := http.ParseTime(value)
	if err != nil || !when.After(now) {
		return 0
	}
	return when.Sub(now)
}
