package events

import (
	"context"
	"testing"
	"time"
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
