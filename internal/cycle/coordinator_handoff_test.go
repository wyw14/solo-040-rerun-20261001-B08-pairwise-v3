package cycle

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/gate"
	"github.com/wyw14/cry-96/internal/hydraulics"
	"github.com/wyw14/cry-96/internal/journal"
	"github.com/wyw14/cry-96/internal/model"
	"github.com/wyw14/cry-96/internal/signal"
)

const testChamber = "chamber-1"

type harness struct {
	coordinator *Coordinator
	publisher   *signal.Publisher
	signals     *signal.Store
	vessels     []model.Vessel
	now         time.Time
}

func newHarness(t *testing.T, vesselCount int) *harness {
	t.Helper()
	base := time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	now := func() time.Time { return base }
	store := journal.NewMemoryStore()
	recorder := journal.NewRecorder(store, now)
	gateState := gate.NewState(testChamber)
	gateCommands := gate.NewCommands()
	gateController := gate.NewController(gateState, gateCommands, recorder, now)
	hydraulicsController := hydraulics.NewController(
		gateCommands, recorder, hydraulics.NewRamp(time.Millisecond, now), now,
	)
	signalStore := signal.NewStore()
	coordinator := NewCoordinator(gateController, hydraulicsController, signalStore, recorder, now)
	publisher := signal.NewPublisher(signalStore, recorder, now)

	vessels := make([]model.Vessel, 0, vesselCount)
	for index := 0; index < vesselCount; index++ {
		vessel, err := model.NewVessel(
			"Vessel-"+string(rune('A'+index)), "CALL-"+string(rune('A'+index)), 10+float64(index),
		)
		if err != nil {
			t.Fatalf("vessel %d: %v", index, err)
		}
		vessels = append(vessels, vessel)
	}
	return &harness{
		coordinator: coordinator, publisher: publisher, signals: signalStore,
		vessels: vessels, now: base,
	}
}

func (h *harness) start(t *testing.T) model.TransitCycle {
	t.Helper()
	plan := model.ConvoyPlan{
		ID: uuid.New(), ChamberID: testChamber,
		Direction: model.DirectionUpstream, Vessels: h.vessels, CreatedAt: h.now,
	}
	cycleValue, err := h.coordinator.Start(context.Background(), plan)
	if err != nil {
		t.Fatalf("start transit: %v", err)
	}
	return cycleValue
}

// settleGate drives one queued gate command to its acknowledgement, advancing
// the transit through planned -> admitting -> sealed -> leveling -> releasing.
func (h *harness) settleGate(t *testing.T, open bool) {
	t.Helper()
	ctx := context.Background()
	cycleValue, ok := h.coordinator.Current(testChamber)
	if !ok {
		t.Fatal("active transit not found")
	}
	command, err := h.coordinator.gates.Request(ctx, cycleValue, model.DirectionUpstream, open)
	if err != nil {
		t.Fatalf("issue gate command: %v", err)
	}
	ack := model.DeviceAck{
		CommandID: command.ID, CycleID: command.CycleID, ChamberID: command.ChamberID,
		Generation: command.Generation, Epoch: command.Epoch, Device: command.Device,
		Settled: true, ObservedAt: h.now,
	}
	if err := h.coordinator.ApplyGateAck(ctx, ack); err != nil {
		t.Fatalf("settle gate command: %v", err)
	}
}

func (h *harness) advanceToReleasing(t *testing.T) model.TransitCycle {
	t.Helper()
	for open, index := 0, 0; index < 4; index++ {
		h.settleGate(t, open%2 == 0)
		open++
	}
	cycleValue, ok := h.coordinator.Current(testChamber)
	if !ok {
		t.Fatal("active transit not found after gate sequence")
	}
	if cycleValue.Stage != model.StageReleasing {
		t.Fatalf("expected releasing stage, got %s", cycleValue.Stage)
	}
	return cycleValue
}

func (h *harness) publish(t *testing.T, cycleValue model.TransitCycle, vessels []model.Vessel) {
	t.Helper()
	if _, err := h.publisher.Publish(context.Background(), cycleValue, vessels, model.SignalProceed); err != nil {
		t.Fatalf("publish release signals: %v", err)
	}
}

func (h *harness) handoff(cycleValue model.TransitCycle) error {
	return h.coordinator.ApplySignalHandoff(context.Background(), testChamber, cycleValue.ID, cycleValue.Generation)
}

func TestSignalHandoffWithoutAnyPublicationStaysReleasing(t *testing.T) {
	h := newHarness(t, 2)
	cycleValue := h.start(t)
	cycleValue = h.advanceToReleasing(t)

	if err := h.handoff(cycleValue); err == nil {
		t.Fatal("handoff without release signals must be rejected")
	}
	current, _ := h.coordinator.Current(testChamber)
	if current.Stage != model.StageReleasing {
		t.Fatalf("transit must remain releasing, got %s", current.Stage)
	}
}

func TestSignalHandoffIgnoresAdmissionSignalling(t *testing.T) {
	h := newHarness(t, 1)
	cycleValue := h.start(t)
	cycleValue = h.advanceToReleasing(t)

	// Admission grants a proceed aspect directly through the store; that is not
	// a release publication and must not be allowed to complete the transit.
	h.signals.Set(h.vessels[0].ID, cycleValue.ID, cycleValue.Generation, model.SignalProceed, h.now)
	if err := h.handoff(cycleValue); err == nil {
		t.Fatal("admission-only proceed signal must not satisfy the release handoff")
	}
	current, _ := h.coordinator.Current(testChamber)
	if current.Stage != model.StageReleasing {
		t.Fatalf("transit must remain releasing, got %s", current.Stage)
	}
}

func TestSignalHandoffRejectsMissingConvoyMember(t *testing.T) {
	h := newHarness(t, 2)
	cycleValue := h.start(t)
	cycleValue = h.advanceToReleasing(t)

	// Publish to only one of the two convoy members.
	h.publish(t, cycleValue, h.vessels[:1])

	err := h.handoff(cycleValue)
	if err == nil {
		t.Fatal("handoff with an unreleased convoy member must be rejected")
	}
	current, _ := h.coordinator.Current(testChamber)
	if current.Stage != model.StageReleasing {
		t.Fatalf("transit must remain releasing, got %s", current.Stage)
	}
}

func TestSignalHandoffBeforeReleasingIsRejected(t *testing.T) {
	h := newHarness(t, 1)
	h.start(t)
	// Drive only to leveling (three gate acks), then publish the full convoy.
	for index := 0; index < 3; index++ {
		h.settleGate(t, index%2 == 0)
	}
	current0, _ := h.coordinator.Current(testChamber)
	h.publish(t, current0, h.vessels)

	if err := h.handoff(current0); err == nil {
		t.Fatal("handoff must only be accepted in releasing stage")
	}
	current, _ := h.coordinator.Current(testChamber)
	if current.Stage == model.StageCompleted {
		t.Fatal("transit must not complete outside releasing stage")
	}
}

func TestSignalHandoffRejectsWrongGeneration(t *testing.T) {
	h := newHarness(t, 2)
	cycleValue := h.start(t)
	cycleValue = h.advanceToReleasing(t)
	h.publish(t, cycleValue, h.vessels)

	ctx := context.Background()
	if err := h.coordinator.ApplySignalHandoff(ctx, testChamber, cycleValue.ID, cycleValue.Generation+1); err == nil {
		t.Fatal("handoff carrying a newer generation must be rejected")
	}
	if cycleValue.Generation > 1 {
		t.Fatalf("test setup expected first generation, got %d", cycleValue.Generation)
	}
	if err := h.coordinator.ApplySignalHandoff(ctx, testChamber, uuid.New(), cycleValue.Generation); err == nil {
		t.Fatal("handoff carrying a foreign cycle id must be rejected")
	}
	current, _ := h.coordinator.Current(testChamber)
	if current.Stage != model.StageReleasing {
		t.Fatalf("transit must remain releasing, got %s", current.Stage)
	}
}

func TestSignalHandoffRejectsStaleGenerationAfterRestart(t *testing.T) {
	h := newHarness(t, 1)
	first := h.start(t)
	if _, err := h.coordinator.Cancel(context.Background(), testChamber, "operator cancellation"); err != nil {
		t.Fatalf("cancel first transit: %v", err)
	}
	second := h.start(t) // same chamber, new cycle id and generation 2

	// Field submits a handoff for the previous generation.
	err := h.coordinator.ApplySignalHandoff(context.Background(), testChamber, first.ID, first.Generation)
	if err == nil {
		t.Fatal("stale generation handoff must be rejected")
	}
	current, _ := h.coordinator.Current(testChamber)
	if current.ID != second.ID || current.Generation != second.Generation {
		t.Fatalf("current transit must remain generation 2, got %s gen %d", current.ID, current.Generation)
	}
}

func TestSignalHandoffCompletesExactlyOnceForFullConvoy(t *testing.T) {
	h := newHarness(t, 2)
	cycleValue := h.start(t)
	cycleValue = h.advanceToReleasing(t)
	h.publish(t, cycleValue, h.vessels)

	if err := h.handoff(cycleValue); err != nil {
		t.Fatalf("handoff after full release publication must succeed, got %v", err)
	}
	current, _ := h.coordinator.Current(testChamber)
	if current.Stage != model.StageCompleted {
		t.Fatalf("transit must be completed, got %s", current.Stage)
	}

	// The legitimate handoff is one-shot: a duplicate confirmation must not be
	// recorded again or reopen the transit.
	if err := h.handoff(cycleValue); err == nil {
		t.Fatal("duplicate handoff against a completed transit must be rejected")
	}
}

func TestSignalServiceConfirmHandoffEndToEnd(t *testing.T) {
	h := newHarness(t, 2)
	cycleValue := h.start(t)
	cycleValue = h.advanceToReleasing(t)

	signals := signal.NewService(h.signals)
	signals.BindTransitLookup(h.coordinator)
	confirm := func(cycleValue model.TransitCycle) error {
		return signals.ConfirmHandoff(context.Background(), signal.Handoff{
			ChamberID: testChamber, CycleID: cycleValue.ID, Generation: cycleValue.Generation,
		})
	}

	// No publication at all.
	if err := confirm(cycleValue); err == nil {
		t.Fatal("confirm with no release signals must fail")
	}
	// Only one of two members released.
	h.publish(t, cycleValue, h.vessels[:1])
	if err := confirm(cycleValue); err == nil {
		t.Fatal("confirm with a missing member must fail")
	}
	if current, _ := h.coordinator.Current(testChamber); current.Stage != model.StageReleasing {
		t.Fatalf("transit must remain releasing, got %s", current.Stage)
	}
	// Release the remaining member; full convoy completes exactly once.
	h.publish(t, cycleValue, h.vessels[1:])
	if err := confirm(cycleValue); err != nil {
		t.Fatalf("confirm after full release must succeed, got %v", err)
	}
	if current, _ := h.coordinator.Current(testChamber); current.Stage != model.StageCompleted {
		t.Fatalf("transit must be completed, got %s", current.Stage)
	}
	if err := confirm(cycleValue); err == nil {
		t.Fatal("duplicate confirmation must be rejected")
	}
	if len(signals.History()) != 1 {
		t.Fatalf("exactly one handoff may be recorded, got %d", len(signals.History()))
	}

	// A handoff carrying a stale generation is rejected at the service boundary.
	if err := signals.ConfirmHandoff(context.Background(), signal.Handoff{
		ChamberID: testChamber, CycleID: cycleValue.ID, Generation: cycleValue.Generation + 1,
	}); err == nil {
		t.Fatal("stale generation handoff must be rejected")
	}
}
