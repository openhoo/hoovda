package events

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/openhoo/hoovda/internal/model"
)

type Kind string

type Provenance string

const (
	KindSpeech         Kind = "speech"
	KindBraille        Kind = "braille"
	KindFocus          Kind = "focus"
	KindMode           Kind = "mode"
	KindLiveRegion     Kind = "liveRegion"
	KindAudio          Kind = "audio"
	KindCommandStarted Kind = "commandStarted"
	KindCommandSettled Kind = "commandSettled"
)

const (
	ProvenanceScreenReaderOutput Provenance = "screenReaderOutput"
	ProvenanceScreenReaderEvent  Provenance = "screenReaderEvent"
	ProvenanceAccessibilityEvent Provenance = "accessibilityEvent"
	ProvenanceAdapterLifecycle   Provenance = "adapterLifecycle"
	ProvenanceSynthesizedAudio   Provenance = "synthesizedAudio"
)

type SpeechCommand struct {
	Kind  string `json:"kind"`
	Value string `json:"value,omitempty"`
}

type Event struct {
	Sequence        uint64          `json:"sequence"`
	MonotonicNS     int64           `json:"monotonicNs"`
	Kind            Kind            `json:"kind"`
	SessionID       string          `json:"sessionId,omitempty"`
	CausalCommand   string          `json:"causalCommand,omitempty"`
	Source          *model.ObjectID `json:"source,omitempty"`
	Text            string          `json:"text,omitempty"`
	SpeechCommands  []SpeechCommand `json:"speechCommands,omitempty"`
	BrailleCells    []byte          `json:"brailleCells,omitempty"`
	BrailleCursor   int             `json:"brailleCursor,omitempty"`
	Mode            string          `json:"mode,omitempty"`
	Reason          string          `json:"reason,omitempty"`
	Priority        string          `json:"priority,omitempty"`
	Provenance      Provenance      `json:"provenance,omitempty"`
	Redacted        bool            `json:"redacted,omitempty"`
	AudioOffsetNS   int64           `json:"audioOffsetNs,omitempty"`
	AudioDurationNS int64           `json:"audioDurationNs,omitempty"`
}

type Store struct {
	mu      sync.RWMutex
	started time.Time
	next    uint64
	limit   int
	events  []Event
	changed chan struct{}
}

func NewStore(limit int) *Store {
	if limit < 1 {
		limit = 1
	}
	return &Store{started: time.Now(), next: 1, limit: limit, changed: make(chan struct{})}
}

func (s *Store) Append(event Event) Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	if event.Provenance == "" {
		event.Provenance = defaultProvenance(event.Kind)
	}
	event.Sequence = s.next
	s.next++
	event.MonotonicNS = time.Since(s.started).Nanoseconds()
	s.events = append(s.events, event)
	if overflow := len(s.events) - s.limit; overflow > 0 {
		copy(s.events, s.events[overflow:])
		s.events = s.events[:s.limit]
	}
	close(s.changed)
	s.changed = make(chan struct{})
	return event
}

func defaultProvenance(kind Kind) Provenance {
	switch kind {
	case KindSpeech, KindBraille:
		return ProvenanceScreenReaderOutput
	case KindFocus, KindLiveRegion:
		return ProvenanceAccessibilityEvent
	case KindMode:
		return ProvenanceScreenReaderEvent
	case KindAudio:
		return ProvenanceSynthesizedAudio
	case KindCommandStarted, KindCommandSettled:
		return ProvenanceAdapterLifecycle
	default:
		return ProvenanceAdapterLifecycle
	}
}

func (s *Store) Cursor() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.next - 1
}

func (s *Store) MonotonicNS() int64 {
	return time.Since(s.started).Nanoseconds()
}

func (s *Store) Snapshot(after uint64, sessionID string) ([]Event, uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	current := s.next - 1
	if after > current {
		return nil, current, errors.New("event cursor is ahead of store")
	}
	if len(s.events) > 0 && after < s.events[0].Sequence-1 {
		return nil, current, errors.New("event history was truncated")
	}
	result := make([]Event, 0)
	for _, event := range s.events {
		if event.Sequence > after && (sessionID == "" || event.SessionID == "" || event.SessionID == sessionID) {
			result = append(result, event)
		}
	}
	return result, current, nil
}

func (s *Store) Wait(ctx context.Context, after uint64, sessionID string, quiet time.Duration) ([]Event, uint64, bool, error) {
	var lastCount int
	var quietTimer *time.Timer
	for {
		s.mu.RLock()
		changed := s.changed
		s.mu.RUnlock()
		events, cursor, err := s.Snapshot(after, sessionID)
		if err != nil {
			return nil, cursor, false, err
		}
		if len(events) > 0 && len(events) == lastCount {
			if quietTimer == nil {
				quietTimer = time.NewTimer(quiet)
			}
		} else if len(events) != lastCount {
			lastCount = len(events)
			if quietTimer != nil {
				if !quietTimer.Stop() {
					select {
					case <-quietTimer.C:
					default:
					}
				}
				quietTimer.Reset(quiet)
			} else if len(events) > 0 {
				quietTimer = time.NewTimer(quiet)
			}
		}

		var quietC <-chan time.Time
		if quietTimer != nil {
			quietC = quietTimer.C
		}
		select {
		case <-ctx.Done():
			if quietTimer != nil {
				quietTimer.Stop()
			}
			events, cursor, snapshotErr := s.Snapshot(after, sessionID)
			if snapshotErr != nil {
				return nil, cursor, true, snapshotErr
			}
			return events, cursor, true, nil
		case <-quietC:
			events, cursor, snapshotErr := s.Snapshot(after, sessionID)
			return events, cursor, false, snapshotErr
		case <-changed:
		}
	}
}

func (s *Store) WaitFor(ctx context.Context, after uint64, sessionID string, match func(Event) bool) ([]Event, uint64, bool, error) {
	if match == nil {
		return nil, s.Cursor(), false, errors.New("event predicate is nil")
	}
	for {
		s.mu.RLock()
		changed := s.changed
		s.mu.RUnlock()
		result, cursor, err := s.Snapshot(after, sessionID)
		if err != nil {
			return nil, cursor, false, err
		}
		for _, event := range result {
			if match(event) {
				return result, cursor, true, nil
			}
		}
		select {
		case <-ctx.Done():
			result, cursor, snapshotErr := s.Snapshot(after, sessionID)
			return result, cursor, false, snapshotErr
		case <-changed:
		}
	}
}
