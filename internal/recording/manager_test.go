package recording

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
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

func TestAudioTimelineMatchesRoundedAndSerializedPCM(t *testing.T) {
	manager, err := NewManager(Config{Root: t.TempDir(), Display: ":99", Width: 800, Height: 600})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background(), "indexed", 9_000_000_000, false); err != nil {
		t.Fatal(err)
	}
	// Complete synthesis out of order. The second request overlaps the first;
	// exported PCM serializes it after the first utterance, not at its request time.
	manager.AddAudio("indexed", 9_000_100_000, synth.Audio{SampleRate: 10000, Channels: 1, BitsPerSample: 16, PCM: []byte{3, 0, 4, 0}})
	manager.AddAudio("indexed", 9_000_050_000, synth.Audio{SampleRate: 10000, Channels: 1, BitsPerSample: 16, PCM: []byte{1, 0, 2, 0}})
	index, err := manager.AudioTimeline("indexed")
	if err != nil {
		t.Fatal(err)
	}
	if len(index) != 2 || index[0].MonotonicNS != 9_000_050_000 || index[0].StartSample != 1 || index[0].SampleCount != 2 || index[1].StartSample != 3 || index[1].SampleRate != 10000 {
		t.Fatalf("sample index = %#v", index)
	}
	artifacts, err := manager.Finish(context.Background(), "indexed")
	if err != nil {
		t.Fatal(err)
	}
	for _, artifact := range artifacts {
		if artifact.Name != "screenreader-audio" {
			continue
		}
		wav, err := os.ReadFile(artifact.Path)
		if err != nil {
			t.Fatal(err)
		}
		for i, entry := range index {
			start := 44 + int(entry.StartSample)*2
			if wav[start] != byte(i*2+1) || wav[start+2] != byte(i*2+2) {
				t.Fatalf("index does not point to original PCM: %v", wav[44:])
			}
		}
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
	first := synth.Audio{SampleRate: 10000, Channels: 1, BitsPerSample: 16, PCM: []byte{1, 1}}
	second := synth.Audio{SampleRate: 10000, Channels: 1, BitsPerSample: 16, PCM: []byte{2, 2}}
	audio, err := renderTimeline([]Segment{
		{Offset: 200 * time.Microsecond, Audio: second},
		{Offset: 0, Audio: first},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := audio.PCM; len(got) != 6 || got[0] != 1 || got[1] != 1 || got[4] != 2 || got[5] != 2 {
		t.Fatalf("PCM = %v", got)
	}
}

func TestStartFailureCanBeRetried(t *testing.T) {
	manager, err := NewManager(Config{Root: t.TempDir(), Display: ":99", Width: 800, Height: 600, FFmpeg: "/nonexistent/ffmpeg"})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background(), "retry-start", 0, true); err == nil {
		t.Fatal("expected capture start failure")
	}
	if err := manager.Start(context.Background(), "retry-start", 0, false); err != nil {
		t.Fatalf("retry start: %v", err)
	}
}

func TestSessionPathsRejectTraversal(t *testing.T) {
	manager, err := NewManager(Config{Root: t.TempDir(), Display: ":99", Width: 800, Height: 600})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"../outside-hoovda", "..", ".", "/tmp/hoovda"} {
		if err := manager.Start(context.Background(), id, 0, false); err == nil {
			t.Errorf("Start accepted %q", id)
		}
		if path, err := manager.ResolveArtifact(id, "screenreader-audio"); err == nil {
			t.Errorf("ResolveArtifact accepted %q: %s", id, path)
		}
	}
}

func TestFinishFailureRetainsSessionForRetry(t *testing.T) {
	root := t.TempDir()
	manager, err := NewManager(Config{Root: root, Display: ":99", Width: 800, Height: 600})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background(), "retry-finish", 0, false); err != nil {
		t.Fatal(err)
	}
	wavPath := root + "/retry-finish/screenreader.wav"
	if err := os.Mkdir(wavPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Finish(context.Background(), "retry-finish"); err == nil || errors.Is(err, ErrCaptureFailed) {
		t.Fatal("expected WAV write failure")
	}
	if err := os.Remove(wavPath); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Finish(context.Background(), "retry-finish"); err != nil {
		t.Fatalf("finish retry: %v", err)
	}
}

func TestRecordedAudioOwnsPCM(t *testing.T) {
	manager, err := NewManager(Config{Root: t.TempDir(), Display: ":99", Width: 800, Height: 600})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background(), "owned-pcm", 0, false); err != nil {
		t.Fatal(err)
	}
	pcm := []byte{1, 0, 2, 0}
	manager.AddAudio("owned-pcm", 0, synth.Audio{SampleRate: 10000, Channels: 1, BitsPerSample: 16, PCM: pcm})
	pcm[0] = 99
	artifacts, err := manager.Finish(context.Background(), "owned-pcm")
	if err != nil {
		t.Fatal(err)
	}
	wav, err := os.ReadFile(artifacts[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := synth.DecodeWAV(wav)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.PCM[0] != 1 {
		t.Fatalf("caller mutation changed recording: %v", decoded.PCM)
	}
}

func TestRenderTimelineRejectsMalformedPCM(t *testing.T) {
	for _, audio := range []synth.Audio{
		{SampleRate: 0, Channels: 1, BitsPerSample: 16, PCM: []byte{1, 0}},
		{SampleRate: 10000, Channels: 1, BitsPerSample: 16, PCM: []byte{1}},
	} {
		if _, err := renderTimeline([]Segment{{Audio: audio}}); err == nil {
			t.Errorf("accepted malformed audio: %#v", audio)
		}
	}
}

func TestSealedTimelineMatchesFinalWAVDespiteLateAudio(t *testing.T) {
	manager, err := NewManager(Config{Root: t.TempDir(), Display: ":99", Width: 800, Height: 600})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background(), "sealed", 0, false); err != nil {
		t.Fatal(err)
	}
	manager.AddAudio("sealed", 0, synth.Audio{SampleRate: 10000, Channels: 1, BitsPerSample: 16, PCM: []byte{1, 0}})
	index, err := manager.SealAudioTimeline("sealed")
	if err != nil {
		t.Fatal(err)
	}
	manager.AddAudio("sealed", 100_000, synth.Audio{SampleRate: 10000, Channels: 1, BitsPerSample: 16, PCM: []byte{2, 0}})
	if _, err := manager.WriteJSON("sealed", "screenreader-events", []byte("{}\n")); err != nil {
		t.Fatal(err)
	}
	artifacts, err := manager.Finish(context.Background(), "sealed")
	if err != nil {
		t.Fatal(err)
	}
	for _, artifact := range artifacts {
		if artifact.Name != "screenreader-audio" {
			continue
		}
		wav, err := os.ReadFile(artifact.Path)
		if err != nil {
			t.Fatal(err)
		}
		audio, err := synth.DecodeWAV(wav)
		if err != nil {
			t.Fatal(err)
		}
		if len(index) != 1 || index[0].SampleCount != 1 || len(audio.PCM) != 2 || audio.PCM[0] != 1 {
			t.Fatalf("sealed index diverged from final PCM: %v, %v", index, audio.PCM)
		}
	}
}

func TestMuxFailureRetriesWithoutRestartingOrRewaitingCapture(t *testing.T) {
	directory := t.TempDir()
	command := filepath.Join(directory, "ffmpeg")
	marker := filepath.Join(directory, "mux-failed")
	script := "#!/bin/sh\nfor output in \"$@\"; do :; done\nif test \"$1\" = -nostdin; then\n if test ! -f \"$HOOVDA_MUX_MARKER\"; then printf failed > \"$HOOVDA_MUX_MARKER\"; exit 1; fi\n printf video > \"$output\"\nelse\n read quit\n printf capture > \"$output\"\nfi\n"
	if err := os.WriteFile(command, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOOVDA_MUX_MARKER", marker)
	manager, err := NewManager(Config{Root: filepath.Join(directory, "artifacts"), Display: ":99", Width: 800, Height: 600, FFmpeg: command})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background(), "mux-retry", 0, true); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Finish(context.Background(), "mux-retry"); err == nil || errors.Is(err, ErrCaptureFailed) {
		t.Fatal("expected first mux failure")
	}
	artifacts, err := manager.Finish(context.Background(), "mux-retry")
	if err != nil {
		t.Fatalf("retry after mux failure: %v", err)
	}
	if len(artifacts) != 2 || artifacts[1].Name != "screenreader-video" {
		t.Fatalf("retried artifacts = %#v", artifacts)
	}
}

func TestMuxPreservesCompleteFiniteAudioAndVideo(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("real FFmpeg is required for the media-duration regression")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("real ffprobe is required to verify original video packets")
	}
	for _, test := range []struct {
		name          string
		videoDuration time.Duration
		audioDuration time.Duration
	}{
		{"short-video-long-audio", 400 * time.Millisecond, 2 * time.Second},
		{"long-video-short-audio", 2 * time.Second, 400 * time.Millisecond},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			directory := t.TempDir()
			capture := filepath.Join(directory, "capture.webm")
			output, err := exec.CommandContext(ctx, ffmpeg, "-nostdin", "-v", "error", "-y", "-f", "lavfi", "-i", "color=size=32x32:rate=10:color=blue", "-t", strconv.FormatFloat(test.videoDuration.Seconds(), 'f', 3, 64), "-an", "-c:v", "libvpx-vp9", "-deadline", "realtime", "-cpu-used", "8", capture).CombinedOutput()
			if err != nil {
				t.Fatalf("create VP9 capture: %v: %s", err, output)
			}
			// Supply a deterministic captured stream while exercising Finish's
			// actual FFmpeg mux process, without requiring an X11 display.
			driver := filepath.Join(directory, "capture-driver")
			script := "#!/bin/sh\nif test \"$1\" = -nostdin; then exec \"$HOOVDA_REAL_FFMPEG\" \"$@\"; fi\nfor output in \"$@\"; do :; done\ncp \"$HOOVDA_CAPTURE_FIXTURE\" \"$output\" || exit 1\nread quit\n"
			if err := os.WriteFile(driver, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOOVDA_REAL_FFMPEG", ffmpeg)
			t.Setenv("HOOVDA_CAPTURE_FIXTURE", capture)
			manager, err := NewManager(Config{Root: filepath.Join(directory, "artifacts"), Display: ":99", Width: 32, Height: 32, FFmpeg: driver})
			if err != nil {
				t.Fatal(err)
			}
			if err := manager.Start(ctx, "finite-media", 0, true); err != nil {
				t.Fatal(err)
			}
			defer func() {
				cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_ = manager.Abort(cleanup, "finite-media")
			}()
			const sampleRate = 22_050
			frames := int(test.audioDuration.Nanoseconds() * sampleRate / int64(time.Second))
			pcm := make([]byte, frames*2)
			for frame := 0; frame < frames; frame++ {
				value := int16(4_000)
				if frame%100 >= 50 {
					value = -value
				}
				binary.LittleEndian.PutUint16(pcm[frame*2:], uint16(value))
			}
			manager.AddAudio("finite-media", 0, synth.Audio{SampleRate: sampleRate, Channels: 1, BitsPerSample: 16, PCM: pcm})
			artifacts, err := manager.Finish(ctx, "finite-media")
			if err != nil {
				t.Fatal(err)
			}
			var video string
			for _, artifact := range artifacts {
				if artifact.Name == "screenreader-video" {
					video = artifact.Path
				}
			}
			if video == "" {
				t.Fatal("missing muxed video artifact")
			}
			packetHashes := func(path string) []string {
				t.Helper()
				output, err := exec.CommandContext(ctx, ffprobe, "-v", "error", "-select_streams", "v:0", "-show_packets", "-show_data_hash", "sha256", "-show_entries", "packet=data_hash", "-of", "json", path).CombinedOutput()
				if err != nil {
					t.Fatalf("probe original video packets: %v: %s", err, output)
				}
				var probe struct {
					Packets []struct {
						Hash string `json:"data_hash"`
					} `json:"packets"`
				}
				if err := json.Unmarshal(output, &probe); err != nil {
					t.Fatal(err)
				}
				hashes := make([]string, len(probe.Packets))
				for i, packet := range probe.Packets {
					if packet.Hash == "" {
						t.Fatal("video packet has no encoded-data hash")
					}
					hashes[i] = packet.Hash
				}
				if len(hashes) == 0 {
					t.Fatal("video has no encoded packets")
				}
				return hashes
			}
			originalPackets, muxedPackets := packetHashes(capture), packetHashes(video)
			if !reflect.DeepEqual(originalPackets, muxedPackets) {
				t.Errorf("mux changed original video packets: input=%d output=%d", len(originalPackets), len(muxedPackets))
			}
			decoded, err := exec.CommandContext(ctx, ffmpeg, "-nostdin", "-v", "error", "-i", video, "-map", "0:a:0", "-f", "s16le", "-acodec", "pcm_s16le", "-ar", strconv.Itoa(sampleRate), "-ac", "1", "-").CombinedOutput()
			if err != nil {
				t.Fatalf("decode muxed original audio: %v: %s", err, decoded)
			}
			decodedFrames := len(decoded) / 2
			difference := decodedFrames - frames
			if difference < 0 {
				difference = -difference
			}
			// Opus/resampling can round the final frame. Truncation or padding
			// by the other stream's duration must never qualify.
			if len(decoded)%2 != 0 || difference > sampleRate/50 {
				t.Errorf("muxed audio duration=%v, original PCM duration=%v", time.Duration(int64(decodedFrames)*int64(time.Second)/sampleRate), test.audioDuration)
			}
			hasOriginalTail := false
			for offset := max(0, len(decoded)-sampleRate/10*2); offset+2 <= len(decoded); offset += 2 {
				value := int16(binary.LittleEndian.Uint16(decoded[offset:]))
				if value > 100 || value < -100 {
					hasOriginalTail = true
					break
				}
			}
			if !hasOriginalTail {
				t.Error("final audio window lost the original tone to padded silence")
			}
			t.Logf("input video=%v, input audio=%v, original video packets=%d, decoded audio frames=%d", test.videoDuration, test.audioDuration, len(originalPackets), decodedFrames)
		})
	}
}

func TestAbortReleasesFailedSessionForArtifactCleanup(t *testing.T) {
	manager, err := NewManager(Config{Root: t.TempDir(), Display: ":99", Width: 800, Height: 600})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background(), "abort", 0, false); err != nil {
		t.Fatal(err)
	}
	manager.AddAudio("abort", 0, synth.Audio{SampleRate: 0, Channels: 1, BitsPerSample: 16, PCM: []byte{1, 0}})
	if _, err := manager.Finish(context.Background(), "abort"); err == nil {
		t.Fatal("expected malformed audio error")
	}
	if err := manager.Abort(context.Background(), "abort"); err != nil {
		t.Fatal(err)
	}
	if err := manager.RemoveArtifacts("abort"); err != nil {
		t.Fatalf("failed session leaked after abort: %v", err)
	}
}

func TestRenderTimelineRejectsOversizedGapBeforeAllocation(t *testing.T) {
	_, _, err := renderTimelineWithIndex([]Segment{{Offset: time.Hour, Audio: synth.Audio{SampleRate: 10000, Channels: 1, BitsPerSample: 16, PCM: []byte{1, 0}}}})
	if err == nil {
		t.Fatal("accepted a gap beyond the WAV budget")
	}
}

func TestCaptureFailureIsIrrecoverable(t *testing.T) {
	command := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(command, []byte("#!/bin/sh\nexit 2\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(Config{Root: t.TempDir(), Display: ":99", Width: 800, Height: 600, FFmpeg: command})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background(), "dead-capture", 0, true); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		_, err := manager.Finish(context.Background(), "dead-capture")
		if !errors.Is(err, ErrCaptureFailed) {
			t.Fatalf("capture failure was offered as recoverable: %v", err)
		}
	}
	if err := manager.Abort(context.Background(), "dead-capture"); !errors.Is(err, ErrCaptureFailed) {
		t.Fatalf("abort lost capture classification: %v", err)
	}
	if err := manager.RemoveArtifacts("dead-capture"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background(), "replacement", 0, false); err != nil {
		t.Fatalf("failed capture blocked replacement: %v", err)
	}
}

func TestKilledCaptureIsIrrecoverableAndPreservesContextError(t *testing.T) {
	command := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(command, []byte("#!/bin/sh\nexec sleep 60\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(Config{Root: t.TempDir(), Display: ":99", Width: 800, Height: 600, FFmpeg: command})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background(), "killed-capture", 0, true); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = manager.Finish(ctx, "killed-capture")
	if !errors.Is(err, ErrCaptureFailed) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("capture kill lost permanent/cancellation identity: %v", err)
	}
	_ = manager.Abort(context.Background(), "killed-capture")
}
