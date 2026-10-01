package signal_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/cycle"
	"github.com/wyw14/cry-96/internal/journal"
	"github.com/wyw14/cry-96/internal/model"
	"github.com/wyw14/cry-96/internal/signal"
)

type stubPlans struct {
	plans map[uuid.UUID]model.ConvoyPlan
}

func (s stubPlans) Get(planID uuid.UUID) (model.ConvoyPlan, bool) {
	plan, ok := s.plans[planID]
	return plan, ok
}

type handoffFixture struct {
	service     *signal.Service
	store       *signal.Store
	coordinator *cycle.Coordinator
	cycle       model.TransitCycle
	plan        model.ConvoyPlan
}

func newHandoffFixture(t *testing.T, vesselCount int) *handoffFixture {
	t.Helper()
	now := func() time.Time { return time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC) }
	vessels := make([]model.Vessel, 0, vesselCount)
	for index := 0; index < vesselCount; index++ {
		vessel, err := model.NewVessel("vessel", "call-sign", 42)
		if err != nil {
			t.Fatalf("build vessel: %v", err)
		}
		vessels = append(vessels, vessel)
	}
	plan, err := model.NewConvoyPlan("chamber-1", model.DirectionUpstream, vessels, now())
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	cycleValue, err := model.NewTransitCycle(plan.ChamberID, plan.ID, plan.Direction, 1, now())
	if err != nil {
		t.Fatalf("build cycle: %v", err)
	}
	for _, stage := range []model.TransitStage{
		model.StageAdmitting, model.StageSealed, model.StageLeveling, model.StageReleasing,
	} {
		if err := cycleValue.Advance(stage, "test", now()); err != nil {
			t.Fatalf("advance to %s: %v", stage, err)
		}
	}
	recorder := journal.NewRecorder(journal.NewMemoryStore(), now)
	coordinator := cycle.NewCoordinator(nil, nil, recorder, now)
	coordinator.Restore(cycleValue)
	store := signal.NewStore()
	service := signal.NewService(store)
	service.BindTransitLookup(coordinator)
	service.BindPlanLookup(stubPlans{plans: map[uuid.UUID]model.ConvoyPlan{plan.ID: plan}})
	return &handoffFixture{
		service: service, store: store, coordinator: coordinator,
		cycle: cycleValue, plan: plan,
	}
}

func (f *handoffFixture) handoff() signal.Handoff {
	return signal.Handoff{
		ChamberID: f.cycle.ChamberID, CycleID: f.cycle.ID,
		Generation: f.cycle.Generation, ObservedAt: time.Now().UTC(),
	}
}

func (f *handoffFixture) publishAll(t *testing.T, generation uint64) {
	t.Helper()
	for _, vessel := range f.plan.Vessels {
		f.store.Set(vessel.ID, f.cycle.ID, generation, model.SignalProceed, time.Now())
	}
}

func (f *handoffFixture) stage(t *testing.T) model.TransitStage {
	t.Helper()
	current, ok := f.coordinator.Current(f.cycle.ChamberID)
	if !ok {
		t.Fatal("expected an active transit")
	}
	return current.Stage
}

func TestConfirmHandoffRejectsEmptyPublication(t *testing.T) {
	fixture := newHandoffFixture(t, 2)
	err := fixture.service.ConfirmHandoff(context.Background(), fixture.handoff())
	if err == nil || !strings.Contains(err.Error(), "no release signal published") {
		t.Fatalf("expected missing release signal error, got %v", err)
	}
	if stage := fixture.stage(t); stage != model.StageReleasing {
		t.Fatalf("transit must stay releasing, got %s", stage)
	}
	if len(fixture.service.History()) != 0 {
		t.Fatal("rejected handoff must not be recorded")
	}
}

func TestConfirmHandoffRejectsMissingVessel(t *testing.T) {
	fixture := newHandoffFixture(t, 2)
	first := fixture.plan.Vessels[0]
	fixture.store.Set(first.ID, fixture.cycle.ID, fixture.cycle.Generation, model.SignalProceed, time.Now())
	err := fixture.service.ConfirmHandoff(context.Background(), fixture.handoff())
	if err == nil || !strings.Contains(err.Error(), "no release signal published") {
		t.Fatalf("expected missing vessel signal error, got %v", err)
	}
	if stage := fixture.stage(t); stage != model.StageReleasing {
		t.Fatalf("transit must stay releasing, got %s", stage)
	}
}

func TestConfirmHandoffRejectsStaleSignalGeneration(t *testing.T) {
	fixture := newHandoffFixture(t, 2)
	fixture.publishAll(t, fixture.cycle.Generation-1)
	err := fixture.service.ConfirmHandoff(context.Background(), fixture.handoff())
	if err == nil || !strings.Contains(err.Error(), "another transit generation") {
		t.Fatalf("expected stale signal generation error, got %v", err)
	}
	if stage := fixture.stage(t); stage != model.StageReleasing {
		t.Fatalf("transit must stay releasing, got %s", stage)
	}
}

func TestConfirmHandoffRejectsStaleHandoffGeneration(t *testing.T) {
	fixture := newHandoffFixture(t, 2)
	fixture.publishAll(t, fixture.cycle.Generation)
	handoff := fixture.handoff()
	handoff.Generation--
	err := fixture.service.ConfirmHandoff(context.Background(), handoff)
	if err == nil || !strings.Contains(err.Error(), "another transit generation") {
		t.Fatalf("expected stale handoff generation error, got %v", err)
	}
	if stage := fixture.stage(t); stage != model.StageReleasing {
		t.Fatalf("transit must stay releasing, got %s", stage)
	}
}

func TestConfirmHandoffCompletesExactlyOnce(t *testing.T) {
	fixture := newHandoffFixture(t, 2)
	fixture.publishAll(t, fixture.cycle.Generation)
	if err := fixture.service.ConfirmHandoff(context.Background(), fixture.handoff()); err != nil {
		t.Fatalf("legitimate handoff must complete the transit: %v", err)
	}
	if stage := fixture.stage(t); stage != model.StageCompleted {
		t.Fatalf("transit must be completed, got %s", stage)
	}
	err := fixture.service.ConfirmHandoff(context.Background(), fixture.handoff())
	if err == nil || !strings.Contains(err.Error(), "cannot be reopened") {
		t.Fatalf("second handoff must be rejected, got %v", err)
	}
	if len(fixture.service.History()) != 1 {
		t.Fatalf("exactly one handoff must be recorded, got %d", len(fixture.service.History()))
	}
	if stage := fixture.stage(t); stage != model.StageCompleted {
		t.Fatalf("transit must remain completed, got %s", stage)
	}
}
