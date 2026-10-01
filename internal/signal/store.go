package signal

import (
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/model"
)

// Store keeps the latest field aspect for each vessel.
type Store struct {
	mu       sync.RWMutex
	states   map[uuid.UUID]model.SignalState
	releases map[releaseKey]map[uuid.UUID]struct{}
}

type releaseKey struct {
	cycleID    uuid.UUID
	generation uint64
}

func NewStore() *Store {
	return &Store{
		states:   make(map[uuid.UUID]model.SignalState),
		releases: make(map[releaseKey]map[uuid.UUID]struct{}),
	}
}

func (s *Store) Set(vesselID, cycleID uuid.UUID, generation uint64, aspect model.SignalAspect, now time.Time) model.SignalState {
	state := model.SignalState{
		VesselID: vesselID, CycleID: cycleID, Generation: generation,
		Aspect: aspect, UpdatedAt: now.UTC(),
	}
	s.mu.Lock()
	s.states[vesselID] = state
	s.mu.Unlock()
	return state
}

func (s *Store) Get(vesselID uuid.UUID) (model.SignalState, bool) {
	s.mu.RLock()
	state, ok := s.states[vesselID]
	s.mu.RUnlock()
	return state, ok
}

func (s *Store) Remove(vesselID uuid.UUID) {
	s.mu.Lock()
	delete(s.states, vesselID)
	s.mu.Unlock()
}

// MarkRelease records that a release (proceed) signal was published to a
// vessel for one exact transit generation. Admission signalling never touches
// this ledger, so it is the authoritative basis for handoff completion.
func (s *Store) MarkRelease(cycleID uuid.UUID, generation uint64, vesselID uuid.UUID) {
	key := releaseKey{cycleID: cycleID, generation: generation}
	s.mu.Lock()
	members, ok := s.releases[key]
	if !ok {
		members = make(map[uuid.UUID]struct{})
		s.releases[key] = members
	}
	members[vesselID] = struct{}{}
	s.mu.Unlock()
}

// RevokeRelease removes a vessel from a generation's release ledger when its
// publication is compensated.
func (s *Store) RevokeRelease(cycleID uuid.UUID, generation uint64, vesselID uuid.UUID) {
	key := releaseKey{cycleID: cycleID, generation: generation}
	s.mu.Lock()
	if members, ok := s.releases[key]; ok {
		delete(members, vesselID)
		if len(members) == 0 {
			delete(s.releases, key)
		}
	}
	s.mu.Unlock()
}

// ReleasedVessels returns the vessels with a published release signal for one
// exact transit generation, sorted by ID for deterministic output.
func (s *Store) ReleasedVessels(cycleID uuid.UUID, generation uint64) []uuid.UUID {
	key := releaseKey{cycleID: cycleID, generation: generation}
	s.mu.RLock()
	members := s.releases[key]
	result := make([]uuid.UUID, 0, len(members))
	for vesselID := range members {
		result = append(result, vesselID)
	}
	s.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool { return result[i].String() < result[j].String() })
	return result
}

func (s *Store) List() []model.SignalState {
	s.mu.RLock()
	result := make([]model.SignalState, 0, len(s.states))
	for _, state := range s.states {
		result = append(result, state)
	}
	s.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool {
		return result[i].VesselID.String() < result[j].VesselID.String()
	})
	return result
}
