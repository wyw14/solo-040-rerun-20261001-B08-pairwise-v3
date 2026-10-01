package signal

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/journal"
	"github.com/wyw14/cry-96/internal/model"
)

func testCycle(vesselCount int) (model.TransitCycle, []model.Vessel) {
	now := time.Date(2026, 9, 30, 9, 0, 0, 0, time.UTC)
	vessels := make([]model.Vessel, 0, vesselCount)
	for index := 0; index < vesselCount; index++ {
		vessel, _ := model.NewVessel("Vessel", "CALL", 10)
		vessels = append(vessels, vessel)
	}
	cycleValue, err := model.NewTransitCycle("chamber-1", uuid.New(), model.DirectionUpstream, 1,
		func() []uuid.UUID {
			ids := make([]uuid.UUID, 0, len(vessels))
			for _, vessel := range vessels {
				ids = append(ids, vessel.ID)
			}
			return ids
		}(), now)
	if err != nil {
		panic(err)
	}
	return cycleValue, vessels
}

func newPublisher() (*Publisher, *Store) {
	now := func() time.Time { return time.Date(2026, 9, 30, 9, 0, 0, 0, time.UTC) }
	recorder := journal.NewRecorder(journal.NewMemoryStore(), now)
	store := NewStore()
	return NewPublisher(store, recorder, now), store
}

func TestPublishRejectsEmptyConvoy(t *testing.T) {
	publisher, store := newPublisher()
	cycleValue, _ := testCycle(1)

	publication, err := publisher.Publish(context.Background(), cycleValue, nil, model.SignalProceed)
	if err == nil {
		t.Fatal("publishing to zero vessels must be rejected")
	}
	if len(publication.Published) != 0 {
		t.Fatal("rejected publication must not report released vessels")
	}
	if len(store.ReleasedVessels(cycleValue.ID, cycleValue.Generation)) != 0 {
		t.Fatal("rejected publication must not mark any release")
	}
	if len(store.List()) != 0 {
		t.Fatal("rejected publication must leave the signal store untouched")
	}
}

func TestPublishMarksReleaseLedgerForProceedOnly(t *testing.T) {
	publisher, store := newPublisher()
	cycleValue, vessels := testCycle(2)

	if _, err := publisher.Publish(context.Background(), cycleValue, vessels, model.SignalProceed); err != nil {
		t.Fatalf("publish proceed: %v", err)
	}
	released := store.ReleasedVessels(cycleValue.ID, cycleValue.Generation)
	if len(released) != 2 {
		t.Fatalf("expected 2 released vessels, got %d", len(released))
	}

	// A stop/hold publication changes the aspect but must not open the gate.
	cycleStop, vesselsStop := testCycle(1)
	if _, err := publisher.Publish(context.Background(), cycleStop, vesselsStop, model.SignalStop); err != nil {
		t.Fatalf("publish stop: %v", err)
	}
	if len(store.ReleasedVessels(cycleStop.ID, cycleStop.Generation)) != 0 {
		t.Fatal("non-proceed publication must not mark a release")
	}
}

func TestCompensateRevokesReleaseLedger(t *testing.T) {
	publisher, store := newPublisher()
	cycleValue, vessels := testCycle(2)

	publication, err := publisher.Publish(context.Background(), cycleValue, vessels, model.SignalProceed)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := publisher.Compensate(context.Background(), cycleValue.ChamberID, publication); err != nil {
		t.Fatalf("compensate: %v", err)
	}
	if len(store.ReleasedVessels(cycleValue.ID, cycleValue.Generation)) != 0 {
		t.Fatal("compensation must revoke the release ledger entries")
	}
	if len(store.List()) != 0 {
		t.Fatal("compensation of fresh signals must remove them from the store")
	}
}

func TestReleasedVesselsIsGenerationScoped(t *testing.T) {
	_, store := newPublisher()
	cycleValue, vessels := testCycle(1)
	store.MarkRelease(cycleValue.ID, cycleValue.Generation, vessels[0].ID)
	store.MarkRelease(cycleValue.ID, cycleValue.Generation+1, vessels[0].ID)

	if got := store.ReleasedVessels(cycleValue.ID, cycleValue.Generation); len(got) != 1 {
		t.Fatalf("expected one release for generation 1, got %d", len(got))
	}
	if got := store.ReleasedVessels(cycleValue.ID, 99); len(got) != 0 {
		t.Fatalf("unknown generation must have no releases, got %d", len(got))
	}
}
