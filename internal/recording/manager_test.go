package recording

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/openhoo/hoovda/internal/synth"
)

func TestRenderTimelineInsertsSilence(t *testing.T) {
	audio, err := renderTimeline([]Segment{{Offset: 100 * time.Millisecond, Audio: synth.Audio{SampleRate: 10000, Channels: 1, BitsPerSample: 16, PCM: make([]byte, 200)}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(audio.PCM) != 2200 {
		t.Fatalf("PCM bytes = %d", len(audio.PCM))
	}
}

func TestWriteJSONRetryReplacesArtifactInventory(t *testing.T) {
	manager, err := NewManager(Config{Root: t.TempDir(), Display: ":99", Width: 800, Height: 600})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background(), "retry", 0, false); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.WriteJSON("retry", "screenreader-events", []byte("first\n")); err != nil {
		t.Fatal(err)
	}
	latest, err := manager.WriteJSON("retry", "screenreader-events", []byte("latest\n"))
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := manager.Finish(context.Background(), "retry")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, artifact := range artifacts {
		if artifact.Name == "screenreader-events" {
			count++
			if artifact.SHA256 != latest.SHA256 {
				t.Fatalf("events digest = %q, want %q", artifact.SHA256, latest.SHA256)
			}
		}
	}
	if count != 1 {
		t.Fatalf("events artifact count = %d: %#v", count, artifacts)
	}
	content, err := os.ReadFile(latest.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "latest\n" {
		t.Fatalf("events content = %q", content)
	}
}

func TestRemoveArtifactsRejectsEscapeAndActiveSession(t *testing.T) {
	root := t.TempDir()
	manager, err := NewManager(Config{Root: root, Display: ":99", Width: 800, Height: 600})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.RemoveArtifacts("../outside"); err == nil {
		t.Fatal("path escape was accepted")
	}
	if err := manager.Start(context.Background(), "active", 0, false); err != nil {
		t.Fatal(err)
	}
	if err := manager.RemoveArtifacts("active"); err == nil {
		t.Fatal("active session removal was accepted")
	}
	if _, err := manager.Finish(context.Background(), "active"); err != nil {
		t.Fatal(err)
	}
	if err := manager.RemoveArtifacts("active"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root + "/active"); !os.IsNotExist(err) {
		t.Fatalf("artifact directory still exists: %v", err)
	}
}

func TestRenderTimelineOrdersAsynchronousSegmentsByOffset(t *testing.T) {
	first := synth.Audio{SampleRate: 1000, Channels: 1, BitsPerSample: 16, PCM: []byte{1, 1}}
	second := synth.Audio{SampleRate: 1000, Channels: 1, BitsPerSample: 16, PCM: []byte{2, 2}}
	audio, err := renderTimeline([]Segment{
		{Offset: 2 * time.Millisecond, Audio: second},
		{Offset: 0, Audio: first},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := audio.PCM; len(got) != 6 || got[0] != 1 || got[1] != 1 || got[4] != 2 || got[5] != 2 {
		t.Fatalf("PCM = %v", got)
	}
}
