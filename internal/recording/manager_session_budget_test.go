package recording

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/openhoo/hoovda/internal/synth"
)

func TestSessionWAVPreservesValidUtterancesBeyondSingleWAVBudget(t *testing.T) {
	manager, err := NewManager(Config{Root: t.TempDir(), Display: ":99", Width: 800, Height: 600})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background(), "long-valid-session", 0, false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Abort(context.Background(), "long-valid-session") })
	// Each utterance is valid untrusted input. Together they exceed the
	// single-utterance WAV budget by only one canonical header.
	const segmentBytes = 32 << 20
	audio := synth.Audio{SampleRate: 22_050, Channels: 1, BitsPerSample: 16, PCM: make([]byte, segmentBytes)}
	audio.PCM[0], audio.PCM[len(audio.PCM)-2] = 1, 42
	audio.Duration = time.Duration(int64(len(audio.PCM)) * int64(time.Second) / int64(audio.SampleRate*2))
	if err := audio.Validate(); err != nil {
		t.Fatalf("individual utterance is invalid: %v", err)
	}
	manager.AddAudio("long-valid-session", 0, audio)
	manager.AddAudio("long-valid-session", int64(time.Second), audio)
	index, err := manager.SealAudioTimeline("long-valid-session")
	if err != nil {
		t.Fatalf("two valid utterances lost their session timeline: %v", err)
	}
	if len(index) != 2 || index[0].StartSample != 0 || index[1].StartSample != segmentBytes/2 || index[1].SampleCount != segmentBytes/2 {
		t.Fatalf("serialized sample index = %#v", index)
	}
	artifacts, err := manager.Finish(context.Background(), "long-valid-session")
	if err != nil {
		t.Fatalf("two valid utterances lost their WAV export: %v", err)
	}
	for _, artifact := range artifacts {
		if artifact.Name != "screenreader-audio" {
			continue
		}
		if artifact.Bytes != segmentBytes*2+44 {
			t.Fatalf("WAV bytes = %d, want %d", artifact.Bytes, segmentBytes*2+44)
		}
		file, err := os.Open(artifact.Path)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		header := make([]byte, 44)
		if _, err := file.ReadAt(header, 0); err != nil {
			t.Fatal(err)
		}
		if string(header[:4]) != "RIFF" || string(header[8:12]) != "WAVE" || binary.LittleEndian.Uint32(header[4:8]) != uint32(artifact.Bytes-8) || binary.LittleEndian.Uint32(header[40:44]) != segmentBytes*2 {
			t.Fatalf("invalid recording WAV header: %v", header)
		}
		for _, offset := range []int64{44, 44 + segmentBytes, 44 + segmentBytes - 2, artifact.Bytes - 2} {
			frame := make([]byte, 2)
			if _, err := file.ReadAt(frame, offset); err != nil {
				t.Fatal(err)
			}
			want := byte(1)
			if offset == 44+segmentBytes-2 || offset == artifact.Bytes-2 {
				want = 42
			}
			if frame[0] != want || frame[1] != 0 {
				t.Fatalf("original PCM frame at %d = %v, want [%d 0]", offset, frame, want)
			}
		}
		return
	}
	t.Fatal("screenreader-audio artifact is missing")
}

func TestSessionAudioBudgetAcceptsLastFrameAndRejectsNextBeforeCopy(t *testing.T) {
	manager, err := NewManager(Config{Root: t.TempDir(), Display: ":99", Width: 800, Height: 600})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background(), "budget-boundary", 0, false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Abort(context.Background(), "budget-boundary") })
	session := manager.sessions["budget-boundary"]
	// Model an already accepted history without allocating 256 MiB for a
	// two-frame boundary check. The real aggregate path is exercised above.
	session.audioBytes = maxSessionPCMBytes - 2
	audio := synth.Audio{SampleRate: 22_050, Channels: 1, BitsPerSample: 16, PCM: []byte{17, 0}}
	manager.AddAudio("budget-boundary", 0, audio)
	if session.audioErr != nil || session.audioBytes != maxSessionPCMBytes || len(session.segments) != 1 {
		t.Fatalf("last permitted frame was rejected: bytes=%d segments=%d err=%v", session.audioBytes, len(session.segments), session.audioErr)
	}
	manager.AddAudio("budget-boundary", 1, audio)
	if session.audioErr == nil || !strings.Contains(session.audioErr.Error(), "recorded PCM exceeds session limit") {
		t.Fatalf("over-budget frame was accepted: %v", session.audioErr)
	}
	if session.audioBytes != maxSessionPCMBytes || len(session.segments) != 1 {
		t.Fatalf("over-budget frame was copied: bytes=%d segments=%d", session.audioBytes, len(session.segments))
	}
	if _, err := manager.SealAudioTimeline("budget-boundary"); err != session.audioErr {
		t.Fatalf("timeline lost the terminal budget error: %v", err)
	}
}

func TestSessionStillRejectsOversizedSingleSynthesisWAV(t *testing.T) {
	audio := synth.Audio{SampleRate: 22_050, Channels: 1, BitsPerSample: 16, PCM: make([]byte, synth.MaxPCMBytes+2)}
	if err := audio.Validate(); err == nil {
		t.Fatal("untrusted single-WAV validation limit was raised")
	}
	if _, err := synth.EncodeWAV(audio); err == nil {
		t.Fatal("default single-WAV encoder limit was raised")
	}
	manager, err := NewManager(Config{Root: t.TempDir(), Display: ":99", Width: 800, Height: 600})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background(), "single-boundary", 0, false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Abort(context.Background(), "single-boundary") })
	manager.AddAudio("single-boundary", 0, audio)
	session := manager.sessions["single-boundary"]
	if session.audioErr == nil || session.audioBytes != 0 || len(session.segments) != 0 {
		t.Fatalf("oversized untrusted WAV reached session storage: bytes=%d segments=%d err=%v", session.audioBytes, len(session.segments), session.audioErr)
	}
}

func TestSessionTimelineAndStreamingRespectExactExportBudget(t *testing.T) {
	audio := synth.Audio{SampleRate: 22_050, Channels: 1, BitsPerSample: 16, PCM: make([]byte, synth.MaxPCMBytes)}
	var segments []Segment
	// Reuse the same immutable valid input slice to exercise the full export
	// boundary without allocating a complete timeline or another WAV buffer.
	for i := 0; i < maxSessionPCMBytes/len(audio.PCM); i++ {
		segments = append(segments, Segment{MonotonicNS: int64(i), Audio: audio})
	}
	remaining := maxSessionPCMBytes - len(segments)*len(audio.PCM)
	segments = append(segments, Segment{Audio: synth.Audio{SampleRate: audio.SampleRate, Channels: 1, BitsPerSample: 16, PCM: make([]byte, remaining)}})
	plan, err := planAudioTimeline(segments)
	if err != nil {
		t.Fatalf("exact session budget was rejected: %v", err)
	}
	if plan.pcmBytes != maxSessionPCMBytes {
		t.Fatalf("planned PCM bytes = %d", plan.pcmBytes)
	}
	output := &countRecordingWrites{}
	if err := streamTimelineWAV(context.Background(), output, plan); err != nil {
		t.Fatal(err)
	}
	if output.bytes != int64(maxSessionPCMBytes)+44 || output.largest > recordingIOChunkBytes {
		t.Fatalf("stream buffered the full recording or lost bytes: bytes=%d largest write=%d", output.bytes, output.largest)
	}
	segments = append(segments, Segment{Audio: synth.Audio{SampleRate: audio.SampleRate, Channels: 1, BitsPerSample: 16, PCM: []byte{1, 0}}})
	if _, err := planAudioTimeline(segments); err == nil {
		t.Fatal("one PCM frame beyond the export budget was accepted")
	}
}

func TestSessionTimelineBoundsCumulativeSilenceBeforeAllocation(t *testing.T) {
	audio := synth.Audio{SampleRate: 8000, Channels: 1, BitsPerSample: 16, PCM: []byte{1, 0}}
	offset := func(pcmBytes int) time.Duration {
		return time.Duration(int64(pcmBytes/2) * int64(time.Second) / int64(audio.SampleRate))
	}
	segments := []Segment{
		{Offset: offset(maxTimelineSilenceBytes / 2), Audio: audio},
		{Offset: offset(maxTimelineSilenceBytes + 2), Audio: audio},
	}
	plan, err := planAudioTimeline(segments)
	if err != nil || plan.pcmBytes != maxTimelineSilenceBytes+4 {
		t.Fatalf("exact cumulative silence budget: bytes=%d err=%v", plan.pcmBytes, err)
	}
	segments = append(segments, Segment{Offset: offset(maxTimelineSilenceBytes + 6), Audio: audio})
	if _, err := planAudioTimeline(segments); err == nil || !strings.Contains(err.Error(), "silence exceeds limit") {
		t.Fatalf("two gaps below the individual budget escaped the cumulative limit: %v", err)
	}
	for _, timestamp := range []time.Duration{-1, time.Duration(1<<63 - 1)} {
		if _, err := planAudioTimeline([]Segment{{Offset: timestamp, Audio: audio}}); err == nil {
			t.Fatalf("hostile timestamp %v was accepted", timestamp)
		}
	}
}

func TestStreamedSessionWAVMatchesCanonicalSmallWAV(t *testing.T) {
	for _, test := range []struct {
		name     string
		segments []Segment
		audio    synth.Audio
	}{
		{name: "empty", audio: synth.Audio{SampleRate: 22_050, Channels: 1, BitsPerSample: 16, PCM: make([]byte, 4410)}},
		{name: "rounded-overlap", segments: []Segment{
			{Offset: 100 * time.Microsecond, Audio: synth.Audio{SampleRate: 10000, Channels: 1, BitsPerSample: 16, PCM: []byte{3, 0, 4, 0}}},
			{Offset: 50 * time.Microsecond, Audio: synth.Audio{SampleRate: 10000, Channels: 1, BitsPerSample: 16, PCM: []byte{1, 0, 2, 0}}},
		}, audio: synth.Audio{SampleRate: 10000, Channels: 1, BitsPerSample: 16, PCM: []byte{0, 0, 1, 0, 2, 0, 3, 0, 4, 0}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, err := planAudioTimeline(test.segments)
			if err != nil {
				t.Fatal(err)
			}
			var actual bytes.Buffer
			if err := streamTimelineWAV(context.Background(), &actual, plan); err != nil {
				t.Fatal(err)
			}
			expected, err := synth.EncodeWAV(test.audio)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(actual.Bytes(), expected) {
				t.Fatal("streamed WAV changed the canonical header, rounding, silence or original samples")
			}
		})
	}
}

func TestStreamedSessionWAVRejectsInvalidSizeAndShortWrites(t *testing.T) {
	for _, size := range []int{-1, maxSessionPCMBytes + 2, int(^uint32(0))} {
		var output bytes.Buffer
		if err := streamTimelineWAV(context.Background(), &output, audioTimelinePlan{sampleRate: 22_050, pcmBytes: size}); err == nil || output.Len() != 0 {
			t.Fatalf("invalid container size %d wrote %d bytes: %v", size, output.Len(), err)
		}
	}
	plan, err := planAudioTimeline(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := streamTimelineWAV(context.Background(), shortRecordingWrites{}, plan); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short output write = %v", err)
	}
}

func TestSessionFinishHonorsExpiredContextAndRetainsFrozenRetry(t *testing.T) {
	manager, err := NewManager(Config{Root: t.TempDir(), Display: ":99", Width: 800, Height: 600})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background(), "cancelled-export", 0, false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Abort(context.Background(), "cancelled-export") })
	audio := synth.Audio{SampleRate: 22_050, Channels: 1, BitsPerSample: 16, PCM: []byte{17, 0}}
	manager.AddAudio("cancelled-export", 0, audio)
	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer cancel()
	if _, err := manager.Finish(ctx, "cancelled-export"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired non-video export = %v", err)
	}
	session := manager.sessions["cancelled-export"]
	if session == nil || !session.sealed || session.finishedArtifacts != nil {
		t.Fatal("cancelled export lost its frozen retry state")
	}
	files, err := os.ReadDir(session.dir)
	if err != nil || len(files) != 0 {
		t.Fatalf("cancelled export published a partial artifact: %v, %v", files, err)
	}
	manager.AddAudio("cancelled-export", 1, audio)
	artifacts, err := manager.Finish(context.Background(), "cancelled-export")
	if err != nil || len(artifacts) != 1 || artifacts[0].Bytes != 46 {
		t.Fatalf("frozen export retry = %#v, %v", artifacts, err)
	}
}

func TestStreamedSessionWAVStopsWithinOneChunkAfterCancellation(t *testing.T) {
	audio := synth.Audio{SampleRate: 22_050, Channels: 1, BitsPerSample: 16, PCM: make([]byte, 3*recordingIOChunkBytes)}
	plan, err := planAudioTimeline([]Segment{{Audio: audio}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := &cancelRecordingWrites{cancel: cancel}
	if err := streamTimelineWAV(ctx, output, plan); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled streaming export = %v", err)
	}
	if output.bytes != 44+recordingIOChunkBytes {
		t.Fatalf("export continued writing after cancellation: %d bytes", output.bytes)
	}
}

type countRecordingWrites struct {
	bytes   int64
	largest int
}

func (w *countRecordingWrites) Write(value []byte) (int, error) {
	w.bytes += int64(len(value))
	w.largest = max(w.largest, len(value))
	return len(value), nil
}

type shortRecordingWrites struct{}

func (shortRecordingWrites) Write(value []byte) (int, error) {
	return max(0, len(value)-1), nil
}

type cancelRecordingWrites struct {
	bytes  int
	cancel context.CancelFunc
}

func (w *cancelRecordingWrites) Write(value []byte) (int, error) {
	w.bytes += len(value)
	if w.bytes > 44 {
		w.cancel()
	}
	return len(value), nil
}
