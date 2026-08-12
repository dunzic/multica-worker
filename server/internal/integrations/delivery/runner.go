package delivery

import (
	"context"
	"errors"
	"fmt"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
)

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
		if claim.Row.ExternalMessageID.Valid {
			return channel.SendResult{MessageID: claim.Row.ExternalMessageID.String}, nil
		}
		return channel.SendResult{}, nil
	}
	result, sendErr := send(ctx)
	if sendErr != nil {
		markErr := recorder.MarkFailed(ctx, claim, classifyError(sendErr))
		if markErr != nil {
			return channel.SendResult{}, errors.Join(sendErr, fmt.Errorf("record delivery failure: %w", markErr))
		}
		return channel.SendResult{}, sendErr
	}
	if result.MessageID == "" {
		err := errors.New("connector returned an empty external message id")
		markErr := recorder.MarkFailed(ctx, claim, "delivery_state_conflict")
		if markErr != nil {
			err = errors.Join(err, fmt.Errorf("record delivery failure: %w", markErr))
		}
		return channel.SendResult{}, err
	}
	if _, err := recorder.MarkDelivered(ctx, claim, result.MessageID); err != nil {
		return channel.SendResult{}, fmt.Errorf("record delivered message %s: %w", result.MessageID, err)
	}
	return result, nil
}

func classifyError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "timeout"
	}
	return "provider_error"
}
