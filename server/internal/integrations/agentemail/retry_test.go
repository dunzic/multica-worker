package agentemail

import (
	"context"
	"errors"
	"testing"
	"time"
)

type providerFunc func(context.Context, NotificationRequest) (NotificationResult, error)

func (f providerFunc) Send(ctx context.Context, request NotificationRequest) (NotificationResult, error) {
	return f(ctx, request)
}

type recordingSleeper struct {
	clock  *fixedClock
	delays []time.Duration
	err    error
}

func (s *recordingSleeper) Sleep(_ context.Context, delay time.Duration) error {
	s.delays = append(s.delays, delay)
	if s.clock != nil {
		s.clock.now = s.clock.now.Add(delay)
	}
	return s.err
}

func testRetryingProvider(t *testing.T, provider EmailProvider, policy RetryPolicy, clock *fixedClock, sleeper *recordingSleeper, jitter JitterFunc) *RetryingProvider {
	t.Helper()
	retrying, err := NewRetryingProvider(provider, policy, RetryDependencies{Clock: clock, Sleeper: sleeper, Jitter: jitter})
	if err != nil {
		t.Fatal(err)
	}
	return retrying
}

func TestRetryingProviderHonorsRetryAfterAndPreservesIdempotency(t *testing.T) {
	clock := &fixedClock{now: time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC)}
	sleeper := &recordingSleeper{clock: clock}
	var keys []string
	provider := providerFunc(func(_ context.Context, request NotificationRequest) (NotificationResult, error) {
		keys = append(keys, request.IdempotencyKey)
		if len(keys) < 3 {
			return NotificationResult{}, deliveryError(FailureDefiniteRetryable, ErrorRateLimited, 11*time.Second, nil)
		}
		return NotificationResult{ProviderMessageID: "provider-id", Status: "accepted"}, nil
	})
	retrying := testRetryingProvider(t, provider, RetryPolicy{MaxAttempts: 3, MaxElapsed: time.Minute, BaseDelay: time.Second, MaxDelay: 10 * time.Second}, clock, sleeper, func(delay time.Duration) time.Duration {
		t.Fatal("jitter must not alter Retry-After")
		return delay
	})

	result, err := retrying.Send(context.Background(), validRequest())
	if err != nil || result.ProviderMessageID != "provider-id" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(keys) != 3 || keys[0] != "multica:message-1" || keys[1] != keys[0] || keys[2] != keys[0] {
		t.Fatalf("idempotency keys = %#v", keys)
	}
	if len(sleeper.delays) != 2 || sleeper.delays[0] != 11*time.Second || sleeper.delays[1] != 11*time.Second {
		t.Fatalf("delays = %#v", sleeper.delays)
	}
}

func TestRetryingProviderUsesInjectedExponentialJitter(t *testing.T) {
	clock := &fixedClock{}
	sleeper := &recordingSleeper{clock: clock}
	var attempts int
	var jitterInputs []time.Duration
	provider := providerFunc(func(context.Context, NotificationRequest) (NotificationResult, error) {
		attempts++
		if attempts < 4 {
			return NotificationResult{}, deliveryError(FailureDefiniteRetryable, ErrorProviderUnavailable, 0, nil)
		}
		return NotificationResult{ProviderMessageID: "id"}, nil
	})
	retrying := testRetryingProvider(t, provider, RetryPolicy{MaxAttempts: 4, MaxElapsed: time.Minute, BaseDelay: 2 * time.Second, MaxDelay: 5 * time.Second}, clock, sleeper, func(delay time.Duration) time.Duration {
		jitterInputs = append(jitterInputs, delay)
		return delay / 2
	})
	_, err := retrying.Send(context.Background(), validRequest())
	if err != nil {
		t.Fatal(err)
	}
	wantInputs := []time.Duration{2 * time.Second, 4 * time.Second, 5 * time.Second}
	wantDelays := []time.Duration{time.Second, 2 * time.Second, 2500 * time.Millisecond}
	for i := range wantInputs {
		if jitterInputs[i] != wantInputs[i] || sleeper.delays[i] != wantDelays[i] {
			t.Fatalf("step %d inputs=%#v delays=%#v", i, jitterInputs, sleeper.delays)
		}
	}
}

func TestRetryingProviderNeverRetriesPermanentOrAmbiguous(t *testing.T) {
	for _, class := range []FailureClass{FailureDefinitePermanent, FailureAmbiguous} {
		t.Run(string(class), func(t *testing.T) {
			clock := &fixedClock{}
			sleeper := &recordingSleeper{clock: clock}
			attempts := 0
			provider := providerFunc(func(context.Context, NotificationRequest) (NotificationResult, error) {
				attempts++
				return NotificationResult{}, deliveryError(class, ErrorProviderRejected, 0, nil)
			})
			retrying := testRetryingProvider(t, provider, RetryPolicy{MaxAttempts: 3, MaxElapsed: time.Minute, BaseDelay: time.Second, MaxDelay: time.Second}, clock, sleeper, func(delay time.Duration) time.Duration { return delay })
			_, err := retrying.Send(context.Background(), validRequest())
			if err == nil || attempts != 1 || len(sleeper.delays) != 0 {
				t.Fatalf("err=%v attempts=%d sleeps=%#v", err, attempts, sleeper.delays)
			}
		})
	}
}

func TestRetryingProviderFreezesInconsistentProviderOutcomesAsAmbiguous(t *testing.T) {
	tests := []struct {
		name   string
		result NotificationResult
		err    error
	}{
		{name: "success without message id", result: NotificationResult{Status: "accepted"}},
		{name: "message id with error", result: NotificationResult{ProviderMessageID: "possibly-accepted"}, err: deliveryError(FailureDefiniteRetryable, ErrorProviderUnavailable, 0, nil)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clock := &fixedClock{}
			sleeper := &recordingSleeper{clock: clock}
			attempts := 0
			provider := providerFunc(func(context.Context, NotificationRequest) (NotificationResult, error) {
				attempts++
				return test.result, test.err
			})
			retrying := testRetryingProvider(t, provider, RetryPolicy{MaxAttempts: 3, MaxElapsed: time.Minute, BaseDelay: time.Second, MaxDelay: time.Second}, clock, sleeper, func(delay time.Duration) time.Duration { return delay })
			result, err := retrying.Send(context.Background(), validRequest())
			deliveryErr := deliveryErrorFrom(t, err)
			if deliveryErr.Class != FailureAmbiguous || deliveryErr.Code != ErrorResponseUnknown || attempts != 1 || len(sleeper.delays) != 0 || result.ProviderMessageID != test.result.ProviderMessageID {
				t.Fatalf("result=%+v error=%+v attempts=%d sleeps=%#v", result, deliveryErr, attempts, sleeper.delays)
			}
		})
	}
}

func TestRetryingProviderStopsAtAttemptAndElapsedLimits(t *testing.T) {
	t.Run("attempt limit", func(t *testing.T) {
		clock := &fixedClock{}
		sleeper := &recordingSleeper{clock: clock}
		attempts := 0
		provider := providerFunc(func(context.Context, NotificationRequest) (NotificationResult, error) {
			attempts++
			return NotificationResult{}, deliveryError(FailureDefiniteRetryable, ErrorProviderUnavailable, 0, nil)
		})
		retrying := testRetryingProvider(t, provider, RetryPolicy{MaxAttempts: 3, MaxElapsed: time.Minute, BaseDelay: time.Second, MaxDelay: time.Second}, clock, sleeper, func(delay time.Duration) time.Duration { return delay })
		_, err := retrying.Send(context.Background(), validRequest())
		deliveryErr := deliveryErrorFrom(t, err)
		if attempts != 3 || deliveryErr.Attempts() != 3 || len(sleeper.delays) != 2 {
			t.Fatalf("attempts=%d reported=%d sleeps=%#v", attempts, deliveryErr.Attempts(), sleeper.delays)
		}
	})

	t.Run("elapsed limit", func(t *testing.T) {
		clock := &fixedClock{}
		sleeper := &recordingSleeper{clock: clock}
		attempts := 0
		provider := providerFunc(func(context.Context, NotificationRequest) (NotificationResult, error) {
			attempts++
			return NotificationResult{}, deliveryError(FailureDefiniteRetryable, ErrorRateLimited, 6*time.Second, nil)
		})
		retrying := testRetryingProvider(t, provider, RetryPolicy{MaxAttempts: 3, MaxElapsed: 5 * time.Second, BaseDelay: time.Second, MaxDelay: time.Second}, clock, sleeper, func(delay time.Duration) time.Duration { return delay })
		_, err := retrying.Send(context.Background(), validRequest())
		if err == nil || attempts != 1 || len(sleeper.delays) != 0 {
			t.Fatalf("err=%v attempts=%d sleeps=%#v", err, attempts, sleeper.delays)
		}
	})
}

func TestRetryingProviderStopsWhenSleepIsCancelled(t *testing.T) {
	clock := &fixedClock{}
	sleeper := &recordingSleeper{clock: clock, err: context.Canceled}
	attempts := 0
	provider := providerFunc(func(context.Context, NotificationRequest) (NotificationResult, error) {
		attempts++
		return NotificationResult{}, deliveryError(FailureDefiniteRetryable, ErrorProviderUnavailable, 0, errors.New("safe cause"))
	})
	retrying := testRetryingProvider(t, provider, RetryPolicy{MaxAttempts: 3, MaxElapsed: time.Minute, BaseDelay: time.Second, MaxDelay: time.Second}, clock, sleeper, func(delay time.Duration) time.Duration { return delay })
	_, err := retrying.Send(context.Background(), validRequest())
	if err == nil || attempts != 1 || len(sleeper.delays) != 1 {
		t.Fatalf("err=%v attempts=%d sleeps=%#v", err, attempts, sleeper.delays)
	}
}
