package signal

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/model"
)

type TransitLookup interface {
	Current(string) (model.TransitCycle, bool)
	ApplySignalHandoff(context.Context, uuid.UUID, uint64) error
}

// PlanLookup resolves the convoy a transit generation must release.
type PlanLookup interface {
	Get(planID uuid.UUID) (model.ConvoyPlan, bool)
}

type Handoff struct {
	ChamberID  string
	CycleID    uuid.UUID
	Generation uint64
	ObservedAt time.Time
}

// Service validates field handoffs before asking cycle to advance.
type Service struct {
	mu       sync.RWMutex
	store    *Store
	lookup   TransitLookup
	plans    PlanLookup
	handoffs []Handoff
}

func NewService(store *Store) *Service { return &Service{store: store} }

func (s *Service) BindTransitLookup(lookup TransitLookup) { s.lookup = lookup }

func (s *Service) BindPlanLookup(plans PlanLookup) { s.plans = plans }

func (s *Service) ConfirmHandoff(ctx context.Context, handoff Handoff) error {
	if s.lookup == nil {
		return errors.New("transit lookup is not bound")
	}
	if s.plans == nil {
		return errors.New("plan lookup is not bound")
	}
	current, ok := s.lookup.Current(handoff.ChamberID)
	if !ok {
		return errors.New("no active transit for chamber")
	}
	if current.Stage.Terminal() {
		return errors.New("completed transit cannot be reopened")
	}
	if !current.IdentityMatches(handoff.CycleID, handoff.Generation) {
		return errors.New("handoff belongs to another transit generation")
	}
	if err := s.requireReleaseSignals(current); err != nil {
		return err
	}
	if err := s.lookup.ApplySignalHandoff(ctx, handoff.CycleID, handoff.Generation); err != nil {
		return err
	}
	s.mu.Lock()
	s.handoffs = append(s.handoffs, handoff)
	s.mu.Unlock()
	return nil
}

// requireReleaseSignals closes the loop between signal publication and transit
// completion: every convoy vessel must hold a proceed signal stamped with the
// current transit generation before a field handoff may complete the transit.
func (s *Service) requireReleaseSignals(current model.TransitCycle) error {
	plan, ok := s.plans.Get(current.PlanID)
	if !ok || plan.Cancelled {
		return errors.New("active convoy plan for transit not found")
	}
	if len(plan.Vessels) == 0 {
		return errors.New("convoy plan has no vessels to release")
	}
	for _, vessel := range plan.Vessels {
		state, ok := s.store.Get(vessel.ID)
		if !ok {
			return fmt.Errorf("no release signal published for vessel %s", vessel.ID)
		}
		if !state.MatchesCycle(current.ID, current.Generation) {
			return fmt.Errorf("release signal for vessel %s belongs to another transit generation", vessel.ID)
		}
		if state.Aspect != model.SignalProceed {
			return fmt.Errorf("vessel %s has not been released to proceed", vessel.ID)
		}
	}
	return nil
}

func (s *Service) History() []Handoff {
	s.mu.RLock()
	result := append([]Handoff(nil), s.handoffs...)
	s.mu.RUnlock()
	return result
}

func (s *Service) States() []model.SignalState { return s.store.List() }
