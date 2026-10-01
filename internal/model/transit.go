package model

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Direction identifies which river reach sends vessels into a chamber.
type Direction string

const (
	DirectionUpstream   Direction = "upstream"
	DirectionDownstream Direction = "downstream"
)

func (d Direction) Valid() bool {
	return d == DirectionUpstream || d == DirectionDownstream
}

func (d Direction) Opposite() Direction {
	if d == DirectionUpstream {
		return DirectionDownstream
	}
	return DirectionUpstream
}

// TransitStage is the externally visible phase of a lock transit.
type TransitStage string

const (
	StagePlanned   TransitStage = "planned"
	StageAdmitting TransitStage = "admitting"
	StageSealed    TransitStage = "sealed"
	StageLeveling  TransitStage = "leveling"
	StageReleasing TransitStage = "releasing"
	StageCompleted TransitStage = "completed"
	StageCancelled TransitStage = "cancelled"
	StageEmergency TransitStage = "emergency"
)

var stageOrder = map[TransitStage]int{
	StagePlanned: 0, StageAdmitting: 1, StageSealed: 2,
	StageLeveling: 3, StageReleasing: 4, StageCompleted: 5,
}

func (s TransitStage) Terminal() bool {
	return s == StageCompleted || s == StageCancelled || s == StageEmergency
}

func (s TransitStage) CanAdvance(next TransitStage) bool {
	if s.Terminal() {
		return false
	}
	if next == StageCancelled || next == StageEmergency {
		return true
	}
	current, currentOK := stageOrder[s]
	following, nextOK := stageOrder[next]
	return currentOK && nextOK && following == current+1
}

// TransitCycle owns a complete passage and its device-command generation.
type TransitCycle struct {
	ID         uuid.UUID    `json:"id"`
	ChamberID  string       `json:"chamber_id"`
	PlanID     uuid.UUID    `json:"plan_id"`
	Direction  Direction    `json:"direction"`
	Stage      TransitStage `json:"stage"`
	Generation uint64       `json:"generation"`
	Convoy     []uuid.UUID  `json:"convoy"`
	CreatedAt  time.Time    `json:"created_at"`
	UpdatedAt  time.Time    `json:"updated_at"`
	Reason     string       `json:"reason,omitempty"`
}

func NewTransitCycle(chamberID string, planID uuid.UUID, direction Direction, generation uint64, convoy []uuid.UUID, now time.Time) (TransitCycle, error) {
	if chamberID == "" {
		return TransitCycle{}, errors.New("chamber id is required")
	}
	if planID == uuid.Nil {
		return TransitCycle{}, errors.New("plan id is required")
	}
	if !direction.Valid() {
		return TransitCycle{}, fmt.Errorf("invalid direction %q", direction)
	}
	if generation == 0 {
		return TransitCycle{}, errors.New("generation must be positive")
	}
	convoyIDs := normalizeConvoy(convoy)
	if len(convoyIDs) == 0 {
		return TransitCycle{}, errors.New("convoy must contain at least one vessel")
	}
	return TransitCycle{
		ID: uuid.New(), ChamberID: chamberID, PlanID: planID,
		Direction: direction, Stage: StagePlanned, Generation: generation,
		Convoy:    convoyIDs,
		CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
	}, nil
}

// normalizeConvoy returns the de-duplicated, nil-stable roster for a transit.
func normalizeConvoy(convoy []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{}, len(convoy))
	roster := make([]uuid.UUID, 0, len(convoy))
	for _, id := range convoy {
		if id == uuid.Nil {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		roster = append(roster, id)
	}
	return roster
}

func (c TransitCycle) Clone() TransitCycle {
	if len(c.Convoy) > 0 {
		c.Convoy = append([]uuid.UUID(nil), c.Convoy...)
	}
	return c
}

// MissingConvoyMembers returns convoy members absent from the released roster,
// i.e. vessels that have not received a published release signal for this
// exact transit generation.
func (c TransitCycle) MissingConvoyMembers(released []uuid.UUID) []uuid.UUID {
	releasedSet := make(map[uuid.UUID]struct{}, len(released))
	for _, vesselID := range released {
		releasedSet[vesselID] = struct{}{}
	}
	var missing []uuid.UUID
	for _, vesselID := range c.Convoy {
		if _, ok := releasedSet[vesselID]; !ok {
			missing = append(missing, vesselID)
		}
	}
	return missing
}

func (c *TransitCycle) Advance(next TransitStage, reason string, now time.Time) error {
	if !c.Stage.CanAdvance(next) {
		return fmt.Errorf("cannot advance transit %s from %s to %s", c.ID, c.Stage, next)
	}
	c.Stage = next
	c.Reason = reason
	c.UpdatedAt = now.UTC()
	return nil
}

func (c TransitCycle) IdentityMatches(id uuid.UUID, generation uint64) bool {
	return c.ID == id && c.Generation == generation
}
