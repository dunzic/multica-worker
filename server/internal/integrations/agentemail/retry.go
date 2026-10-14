package agentemail

import (
	"context"
	"errors"
	"math/rand/v2"
	"strings"
	"time"
)

// Sleeper supports context-aware, deterministic retry tests.
type Sleeper interface {
	Sleep(context.Context, time.Duration) error
}

type timerSleeper struct{}

func (timerSleeper) Sleep(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type JitterFunc func(time.Duration) time.Duration

// RetryPolicy is a process-local safety net around a single claimed message.
// The durable worker remains responsible for cross-process attempt accounting.
type RetryPolicy struct {
	MaxAttempts int
	MaxElapsed  time.Duration
	BaseDelay   time.Duration
	MaxDelay    time.Duration
}

type RetryDependencies struct {
	Clock   Clock
	Sleeper Sleeper
	Jitter  JitterFunc
}

type RetryingProvider struct {
	provider EmailProvider
	policy   RetryPolicy
	clock    Clock
	sleeper  Sleeper
	jitter   JitterFunc
}

func NewRetryingProvider(provider EmailProvider, policy RetryPolicy, dependencies RetryDependencies) (*RetryingProvider, error) {
	if provider == nil || policy.MaxAttempts < 1 || policy.BaseDelay <= 0 || policy.MaxDelay <= 0 || policy.MaxElapsed <= 0 {
		return nil, errors.New("invalid agent email retry configuration")
	}
	if policy.BaseDelay > policy.MaxDelay {
		return nil, errors.New("agent email retry base delay exceeds maximum delay")
	}
	if dependencies.Clock == nil {
		dependencies.Clock = realClock{}
	}
	if dependencies.Sleeper == nil {
		dependencies.Sleeper = timerSleeper{}
	}
	if dependencies.Jitter == nil {
		dependencies.Jitter = fullJitter
	}
	return &RetryingProvider{
		provider: provider, policy: policy, clock: dependencies.Clock,
		sleeper: dependencies.Sleeper, jitter: dependencies.Jitter,
	}, nil
}

func (p *RetryingProvider) Send(ctx context.Context, request NotificationRequest) (NotificationResult, error) {
	started := p.clock.Now()
	for attempt := 1; attempt <= p.policy.MaxAttempts; attempt++ {
		result, err := p.provider.Send(ctx, request)
		if err == nil {
			if strings.TrimSpace(result.ProviderMessageID) == "" {
				return result, deliveryError(FailureAmbiguous, ErrorResponseUnknown, 0, errors.New("provider returned an empty message id"))
			}
			return result, nil
		}
		if strings.TrimSpace(result.ProviderMessageID) != "" {
			return result, deliveryError(FailureAmbiguous, ErrorResponseUnknown, 0, errors.New("provider returned a message id with an error"))
		}
		deliveryErr, ok := asDeliveryError(err)
		if !ok || deliveryErr.Class != FailureDefiniteRetryable {
			return result, err
		}
		reportedError := *deliveryErr
		reportedError.attempts = attempt
		if attempt == p.policy.MaxAttempts {
			return result, &reportedError
		}

		delay := reportedError.RetryAfter
		if delay <= 0 {
			delay = exponentialDelay(p.policy.BaseDelay, p.policy.MaxDelay, attempt)
			delay = p.jitter(delay)
		}
		if delay < 0 || p.clock.Now().Sub(started)+delay > p.policy.MaxElapsed {
			return result, &reportedError
		}
		if err := p.sleeper.Sleep(ctx, delay); err != nil {
			return result, &reportedError
		}
	}
	panic("unreachable")
}

func exponentialDelay(base, maximum time.Duration, completedAttempts int) time.Duration {
	delay := base
	for i := 1; i < completedAttempts; i++ {
		if delay >= maximum/2 {
			return maximum
		}
		delay *= 2
	}
	if delay > maximum {
		return maximum
	}
	return delay
}

func fullJitter(maximum time.Duration) time.Duration {
	if maximum <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(maximum)))
}
