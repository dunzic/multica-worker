package agentemail

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fixedClock struct{ now time.Time }

func (c *fixedClock) Now() time.Time { return c.now }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func validRequest() NotificationRequest {
	return NotificationRequest{
		To: []string{"one@example.com", "two@example.com"}, Subject: "Daily brief",
		HTMLBody: "<p>sensitive-html</p>", TextBody: "sensitive-text",
		From: "notifications@example.com", IdempotencyKey: "multica:message-1",
	}
}

func deliveryErrorFrom(t *testing.T, err error) *DeliveryError {
	t.Helper()
	var deliveryErr *DeliveryError
	if !errors.As(err, &deliveryErr) {
		t.Fatalf("error type = %T, want *DeliveryError: %v", err, err)
	}
	return deliveryErr
}

func TestResendProviderSendsNotificationContract(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.Method != http.MethodPost || request.URL.Path != "/emails" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer server-secret" {
			t.Errorf("Authorization = %q", got)
		}
		if got := request.Header.Get("Idempotency-Key"); got != "multica:message-1" {
			t.Errorf("Idempotency-Key = %q", got)
		}
		var body resendSendRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if body.From != "notifications@example.com" || len(body.To) != 2 || body.Subject != "Daily brief" ||
			body.HTML != "<p>sensitive-html</p>" || body.Text != "sensitive-text" {
			t.Errorf("provider body = %+v", body)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"id":"provider-message-1"}`))
	}))
	defer server.Close()

	provider, err := newResendProvider(server.Client(), "server-secret", server.URL+"/emails", &fixedClock{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := provider.Send(context.Background(), validRequest())
	if err != nil {
		t.Fatal(err)
	}
	if result.ProviderMessageID != "provider-message-1" || result.Status != "accepted" || calls.Load() != 1 {
		t.Fatalf("result = %+v calls=%d", result, calls.Load())
	}
}

func TestNewResendProviderFromEnvFailsClosedWithoutCredential(t *testing.T) {
	t.Setenv(ResendAPIKeyEnv, "")
	t.Setenv("RESEND_API_KEY", "verification-key-must-not-be-reused")
	provider, err := NewResendProviderFromEnv(http.DefaultClient)
	if provider != nil {
		t.Fatal("expected no provider without a server credential")
	}
	deliveryErr := deliveryErrorFrom(t, err)
	if deliveryErr.Class != FailureDefinitePermanent || deliveryErr.Code != ErrorProviderNotConfigured {
		t.Fatalf("error = %+v", deliveryErr)
	}
}

func TestResendProviderClassifiesHTTPFailures(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		retryAfter string
		wantClass  FailureClass
		wantCode   string
		wantDelay  time.Duration
	}{
		{name: "rate limit", status: 429, retryAfter: "17", wantClass: FailureDefiniteRetryable, wantCode: ErrorRateLimited, wantDelay: 17 * time.Second},
		{name: "server error", status: 503, retryAfter: "2", wantClass: FailureDefiniteRetryable, wantCode: ErrorProviderUnavailable, wantDelay: 2 * time.Second},
		{name: "bad request", status: 400, wantClass: FailureDefinitePermanent, wantCode: ErrorProviderRejected},
		{name: "unprocessable", status: 422, wantClass: FailureDefinitePermanent, wantCode: ErrorProviderRejected},
		{name: "unauthorized", status: 401, wantClass: FailureDefinitePermanent, wantCode: ErrorAuthentication},
		{name: "forbidden", status: 403, wantClass: FailureDefinitePermanent, wantCode: ErrorAuthentication},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.Header().Set("Retry-After", test.retryAfter)
				response.WriteHeader(test.status)
				_, _ = response.Write([]byte(`{"message":"provider leaked sensitive-text server-secret"}`))
			}))
			defer server.Close()
			provider, err := newResendProvider(server.Client(), "server-secret", server.URL, &fixedClock{})
			if err != nil {
				t.Fatal(err)
			}
			_, err = provider.Send(context.Background(), validRequest())
			deliveryErr := deliveryErrorFrom(t, err)
			if deliveryErr.Class != test.wantClass || deliveryErr.Code != test.wantCode || deliveryErr.RetryAfter != test.wantDelay {
				t.Fatalf("error = %+v", deliveryErr)
			}
			if got := err.Error(); strings.Contains(got, "sensitive-text") || strings.Contains(got, "server-secret") || strings.Contains(got, "provider leaked") {
				t.Fatalf("safe error exposed request/provider data: %q", got)
			}
		})
	}
}

func TestParseRetryAfterHTTPDateUsesInjectedClock(t *testing.T) {
	now := time.Date(2026, 8, 16, 10, 0, 0, 0, time.UTC)
	value := now.Add(45 * time.Second).Format(http.TimeFormat)
	if got := parseRetryAfter(value, now); got != 45*time.Second {
		t.Fatalf("parseRetryAfter = %s", got)
	}
}

func TestResendProviderTreatsTransportTimeoutAsAmbiguous(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, context.DeadlineExceeded
	})}
	provider, err := newResendProvider(client, "server-secret", "https://resend.invalid/emails", &fixedClock{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Send(context.Background(), validRequest())
	deliveryErr := deliveryErrorFrom(t, err)
	if deliveryErr.Class != FailureAmbiguous || deliveryErr.Code != ErrorResponseUnknown || !errors.Is(deliveryErr.Cause(), context.DeadlineExceeded) {
		t.Fatalf("error = %+v cause=%v", deliveryErr, deliveryErr.Cause())
	}
}

func TestResendProviderTreatsUnknownSuccessResponseAsAmbiguous(t *testing.T) {
	tests := []string{`{"id":""}`, `{not-json}`}
	for _, body := range tests {
		server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			response.Header().Set("Content-Type", "application/json")
			_, _ = response.Write([]byte(body))
		}))
		provider, err := newResendProvider(server.Client(), "server-secret", server.URL, &fixedClock{})
		if err != nil {
			t.Fatal(err)
		}
		_, err = provider.Send(context.Background(), validRequest())
		deliveryErr := deliveryErrorFrom(t, err)
		if deliveryErr.Class != FailureAmbiguous || deliveryErr.Code != ErrorResponseUnknown {
			t.Fatalf("body %q: error = %+v", body, deliveryErr)
		}
		server.Close()
	}
}

func TestResendProviderRejectsInvalidRequestBeforeHTTP(t *testing.T) {
	var calls atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("unexpected")
	})}
	provider, err := newResendProvider(client, "server-secret", "https://resend.invalid/emails", &fixedClock{})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"", strings.Repeat("k", 257), "key\r\ninjected: value"} {
		request := validRequest()
		request.IdempotencyKey = key
		_, err = provider.Send(context.Background(), request)
		deliveryErr := deliveryErrorFrom(t, err)
		if deliveryErr.Class != FailureDefinitePermanent || deliveryErr.Code != ErrorInvalidRequest || calls.Load() != 0 {
			t.Fatalf("key length=%d: error=%+v calls=%d", len(key), deliveryErr, calls.Load())
		}
	}
}
