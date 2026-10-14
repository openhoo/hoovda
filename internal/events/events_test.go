package events

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/openhoo/hoovda/internal/model"
)

func TestStoreOrdersAndFiltersEvents(t *testing.T) {
	store := NewStore(10)
	first := store.Append(Event{Kind: KindSpeech, SessionID: "one", Text: "first"})
	second := store.Append(Event{Kind: KindSpeech, SessionID: "two", Text: "second"})
	if first.Sequence != 1 || second.Sequence != 2 {
		t.Fatalf("sequences = %d, %d", first.Sequence, second.Sequence)
	}
	got, cursor, err := store.Snapshot(0, "one")
	if err != nil || cursor != 2 || len(got) != 1 || got[0].Text != "first" {
		t.Fatalf("snapshot = %#v, %d, %v", got, cursor, err)
	}
}

func TestStoreSessionFilterExcludesAmbientEvents(t *testing.T) {
	store := NewStore(10)
	store.Append(Event{Kind: KindSpeech, Text: "ambient output"})
	store.Append(Event{Kind: KindSpeech, SessionID: "owned", Text: "owned output"})
	items, cursor, err := store.Snapshot(0, "owned")
	if err != nil || cursor != 2 || len(items) != 1 || items[0].Text != "owned output" {
		t.Fatalf("session snapshot included unowned output: items=%#v cursor=%d err=%v", items, cursor, err)
	}
	all, _, err := store.Snapshot(0, "")
	if err != nil || len(all) != 2 {
		t.Fatalf("unfiltered snapshot = %#v, err=%v", all, err)
	}
}

func TestWaitSettlesAfterQuietWindow(t *testing.T) {
	store := NewStore(10)
	store.Append(Event{Kind: KindSpeech, Text: "hello"})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, _, timedOut, err := store.Wait(ctx, 0, "", 5*time.Millisecond)
	if err != nil || timedOut || len(got) != 1 {
		t.Fatalf("wait = %#v, timedOut=%v, err=%v", got, timedOut, err)
	}
}

func TestWaitForIgnoresUnrelatedEvents(t *testing.T) {
	store := NewStore(10)
	store.Append(Event{Kind: KindAudio, SessionID: "test"})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan bool, 1)
	go func() {
		_, _, matched, err := store.WaitFor(ctx, 0, "test", func(event Event) bool {
			return event.Kind == KindCommandSettled && event.CausalCommand == "nextHeading"
		})
		done <- matched && err == nil
	}()
	select {
	case <-done:
		t.Fatal("unrelated event ended wait")
	case <-time.After(10 * time.Millisecond):
	}
	store.Append(Event{Kind: KindCommandSettled, SessionID: "test", CausalCommand: "nextHeading"})
	select {
	case matched := <-done:
		if !matched {
			t.Fatal("matching event was not observed")
		}
	case <-time.After(time.Second):
		t.Fatal("matching event did not end wait")
	}
}

func TestStoreRejectsTruncatedHistory(t *testing.T) {
	store := NewStore(2)
	store.Append(Event{Kind: KindSpeech, Text: "one"})
	store.Append(Event{Kind: KindSpeech, Text: "two"})
	store.Append(Event{Kind: KindSpeech, Text: "three"})
	if _, _, err := store.Snapshot(0, ""); err == nil || err.Error() != "event history was truncated" {
		t.Fatalf("truncated snapshot error = %v", err)
	}
	items, cursor, err := store.Snapshot(1, "")
	if err != nil || cursor != 3 || len(items) != 2 {
		t.Fatalf("retained snapshot = %#v, cursor = %d, err = %v", items, cursor, err)
	}
}

func TestStoreOwnsAppendedAndReturnedPayloads(t *testing.T) {
	store := NewStore(10)
	input := Event{
		Kind: KindSpeech, Source: &model.ObjectID{Bus: "app", Path: "/original"},
		SpeechCommands: []SpeechCommand{{Kind: "text", Value: "original"}},
		BrailleCells:   []byte{1, 2},
	}
	appended := store.Append(input)
	input.Source.Path = "/changed-input"
	input.SpeechCommands[0].Value = "changed input"
	input.BrailleCells[0] = 9
	appended.Source.Path = "/changed-return"
	appended.SpeechCommands[0].Value = "changed return"
	appended.BrailleCells[1] = 9

	items, _, err := store.Snapshot(0, "")
	if err != nil || len(items) != 1 {
		t.Fatalf("snapshot = %#v, err=%v", items, err)
	}
	assertOriginal := func(event Event) {
		t.Helper()
		if event.Source.Path != "/original" || event.SpeechCommands[0].Value != "original" || event.BrailleCells[0] != 1 || event.BrailleCells[1] != 2 {
			t.Fatalf("stored payload was changed through a caller alias: %#v", event)
		}
	}
	assertOriginal(items[0])
	items[0].Source.Path = "/changed-snapshot"
	items[0].SpeechCommands[0].Value = "changed snapshot"
	items[0].BrailleCells[0] = 8
	items, _, err = store.Snapshot(0, "")
	if err != nil {
		t.Fatal(err)
	}
	assertOriginal(items[0])
}

type observedWaitContext struct {
	context.Context
	observed chan struct{}
	once     sync.Once
}

func (c *observedWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.observed) })
	return c.Context.Done()
}

func TestWaitRequiresQuietAfterAppendRacingWithExpiredTimer(t *testing.T) {
	store := NewStore(10)
	store.Append(Event{Kind: KindSpeech, Text: "first"})
	base, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ctx := &observedWaitContext{Context: base, observed: make(chan struct{})}
	const quiet = 20 * time.Millisecond
	done := make(chan []Event, 1)
	go func() {
		items, _, _, _ := store.Wait(ctx, 0, "", quiet)
		done <- items
	}()
	<-ctx.observed // The first snapshot and quiet timer now exist.

	// Queue an append behind a read lock. Once the quiet timer expires, its
	// snapshot waits behind that writer and must recognize the new event.
	store.mu.RLock()
	appended := make(chan Event, 1)
	go func() { appended <- store.Append(Event{Kind: KindSpeech, Text: "second"}) }()
	time.Sleep(2 * quiet)
	store.mu.RUnlock()
	second := <-appended
	items := <-done
	settledAfter := time.Since(store.started) - time.Duration(second.MonotonicNS)
	if len(items) != 2 || settledAfter < quiet {
		t.Fatalf("wait settled %s after last append (quiet=%s), events=%#v", settledAfter, quiet, items)
	}
}

func TestSettleRejectsDeadlineBeforeQuietWindow(t *testing.T) {
	store := NewStore(10)
	store.Append(Event{Kind: KindSpeech, Text: "first"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	_, _, timedOut, err := store.Settle(ctx, 0, "", 20*time.Millisecond)
	if !timedOut || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("settle treated its deadline as quiescence: timedOut=%v err=%v", timedOut, err)
	}
}

type appendOnDoneContext struct {
	context.Context
	once   sync.Once
	append func()
}

func (c *appendOnDoneContext) Done() <-chan struct{} {
	c.once.Do(c.append)
	return c.Context.Done()
}

func TestWaitForMatchesEventObservedAtCancellation(t *testing.T) {
	for attempt := 0; attempt < 32; attempt++ {
		store := NewStore(10)
		base, cancel := context.WithCancel(context.Background())
		cancel()
		ctx := &appendOnDoneContext{Context: base, append: func() {
			store.Append(Event{Kind: KindCommandStarted, SessionID: "test", CausalCommand: "activate"})
		}}
		items, _, matched, err := store.WaitFor(ctx, 0, "test", func(event Event) bool {
			return event.Kind == KindCommandStarted && event.CausalCommand == "activate"
		})
		if err != nil || len(items) != 1 || !matched {
			t.Fatalf("acknowledgement visible at cancellation was treated as missing: matched=%v events=%#v err=%v", matched, items, err)
		}
	}
}
