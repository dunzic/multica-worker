package delivery

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
)

const recordOutcomeTimeout = 5 * time.Second

// DefiniteFailure marks a connector error as a provider rejection that is
// known to have happened before message acceptance. Only these errors may
// enter the automatic retry path. Transport errors are deliberately ambiguous.
func DefiniteFailure(errorCode string, err error) error {
	if err == nil {
		return nil
	}
	return &definiteFailure{errorCode: normalizeErrorCode(errorCode), err: err}
}

type definiteFailure struct {
	errorCode string
	err       error
}

func (e *definiteFailure) Error() string { return e.err.Error() }
func (e *definiteFailure) Unwrap() error { return e.err }

// Send runs one connector send behind the durable claim fence. A duplicate
// event whose delivery is already terminal returns the recorded outcome
// without calling the provider again. The ledger stores no message body.
func Send(ctx context.Context, recorder Recorder, input ClaimInput, send func(context.Context) (channel.SendResult, error)) (channel.SendResult, error) {
	if recorder == nil {
		return send(ctx)
	}
	claim, err := recorder.Claim(ctx, input)
	if err != nil {
		return channel.SendResult{}, err
	}
	if !claim.ShouldSend {
		if claim.Row.Status == "ambiguous" {
			return channel.SendResult{}, ErrAmbiguous
		}
		if claim.Row.ExternalMessageID.Valid {
			return channel.SendResult{MessageID: claim.Row.ExternalMessageID.String}, nil
		}
		return channel.SendResult{}, nil
	}
	result, sendErr := send(ctx)
	if sendErr != nil {
		if code, definite := definiteFailureCode(sendErr); definite && strings.TrimSpace(result.MessageID) == "" {
			recordCtx, cancel := detachedOutcomeContext(ctx)
			markErr := recorder.MarkFailed(recordCtx, claim, code)
			cancel()
			if markErr != nil {
				return result, errors.Join(sendErr, fmt.Errorf("record definite delivery failure: %w", markErr))
			}
			return result, sendErr
		}
		reason := AmbiguityResponseUnknown
		if strings.TrimSpace(result.MessageID) != "" {
			reason = AmbiguityPartialDelivery
		}
		recordCtx, cancel := detachedOutcomeContext(ctx)
		_, markErr := recorder.MarkAmbiguous(recordCtx, claim, reason, result.MessageID)
		cancel()
		if markErr != nil {
			return result, errors.Join(sendErr, ErrAmbiguous, fmt.Errorf("record ambiguous delivery: %w", markErr))
		}
		return result, errors.Join(sendErr, ErrAmbiguous)
	}
	if strings.TrimSpace(result.MessageID) == "" {
		err := errors.Join(errors.New("connector returned an empty external message id"), ErrAmbiguous)
		recordCtx, cancel := detachedOutcomeContext(ctx)
		_, markErr := recorder.MarkAmbiguous(recordCtx, claim, AmbiguityMissingProviderID, "")
		cancel()
		if markErr != nil {
			err = errors.Join(err, fmt.Errorf("record ambiguous delivery: %w", markErr))
		}
		return result, err
	}
	deliveredCtx, cancelDelivered := detachedOutcomeContext(ctx)
	_, deliveredErr := recorder.MarkDelivered(deliveredCtx, claim, result.MessageID)
	cancelDelivered()
	if deliveredErr != nil {
		ambiguousCtx, cancelAmbiguous := detachedOutcomeContext(ctx)
		_, markErr := recorder.MarkAmbiguous(ambiguousCtx, claim, AmbiguityReceiptPersistFailed, result.MessageID)
		cancelAmbiguous()
		if markErr != nil {
			return result, errors.Join(ErrAmbiguous, fmt.Errorf("record delivered message %s: %w", result.MessageID, deliveredErr), fmt.Errorf("record ambiguous delivery: %w", markErr))
		}
		return result, errors.Join(ErrAmbiguous, fmt.Errorf("record delivered message %s: %w", result.MessageID, deliveredErr))
	}
	return result, nil
}

func detachedOutcomeContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), recordOutcomeTimeout)
}

func definiteFailureCode(err error) (string, bool) {
	var definite *definiteFailure
	if errors.As(err, &definite) {
		return definite.errorCode, true
	}
	return "", false
}
