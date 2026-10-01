package cycle

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/model"
)

func nextAfterGate(stage model.TransitStage) (model.TransitStage, error) {
	switch stage {
	case model.StagePlanned:
		return model.StageAdmitting, nil
	case model.StageAdmitting:
		return model.StageSealed, nil
	case model.StageSealed:
		return model.StageLeveling, nil
	case model.StageLeveling:
		return model.StageReleasing, nil
	default:
		return "", errors.New("gate acknowledgement is not valid in current stage")
	}
}

// applySignalTransition completes a transit only after the release-signal loop
// is closed: the field may hand off in releasing stage, and every convoy member
// must have a release signal published for this exact generation.
func applySignalTransition(current *model.TransitCycle, released []uuid.UUID) error {
	if current.Stage.Terminal() {
		return errors.New("terminal transit cannot accept signal handoff")
	}
	if current.Stage != model.StageReleasing {
		return errors.New("signal handoff requires releasing stage")
	}
	if len(current.Convoy) == 0 {
		return errors.New("transit has no convoy roster to confirm")
	}
	if len(released) == 0 {
		return errors.New("no release signal has been published for this transit")
	}
	if missing := current.MissingConvoyMembers(released); len(missing) > 0 {
		return fmt.Errorf("signal handoff rejected: %d convoy member(s) lack a release signal", len(missing))
	}
	return current.Advance(model.StageCompleted, "field signal handoff confirmed", current.UpdatedAt.Add(1))
}

func terminalStage(reason string) model.TransitStage {
	if reason == "emergency" {
		return model.StageEmergency
	}
	return model.StageCancelled
}
