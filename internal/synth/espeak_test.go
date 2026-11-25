package synth

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validSpeechRequest() Request {
	return Request{Text: "hello", Locale: "en-US", Rate: 175, Pitch: 50, Volume: 100}
}

func TestESpeakReturnsContextCancellation(t *testing.T) {
	directory := t.TempDir()
	command := filepath.Join(directory, "espeak-ng")
	marker := filepath.Join(directory, "started")
	script := "#!/bin/sh\nprintf started > \"$HOOVDA_SYNTH_STARTED\"\nexec sleep 60\n"
	if err := os.WriteFile(command, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOOVDA_SYNTH_STARTED", marker)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := (ESpeak{Command: command}).Synthesize(ctx, validSpeechRequest()); done <- err }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("synth process did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation became a synthesis failure: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled synthesis did not exit")
	}
}

func TestESpeakBoundsProcessOutput(t *testing.T) {
	command := filepath.Join(t.TempDir(), "espeak-ng")
	script := "#!/bin/sh\nexec dd if=/dev/zero bs=1048576 count=65 2>/dev/null\n"
	if err := os.WriteFile(command, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := (ESpeak{Command: command}).Synthesize(context.Background(), validSpeechRequest())
	if err == nil || !strings.Contains(err.Error(), "output exceeds limit") {
		t.Fatalf("unbounded subprocess output reached WAV decoder: %v", err)
	}
}

// This is a real audio comparison when the pinned synthesizer is available.
// The positional-argument baseline does not share the stdin framing code.
func TestESpeakPreservesLastSpokenCharacter(t *testing.T) {
	cmd := os.Getenv("HOOVDA_TEST_ESPEAK")
	if cmd == "" {
		var err error
		cmd, err = exec.LookPath("espeak-ng")
		if err != nil {
			t.Skip("eSpeak NG not installed")
		}
	}
	for _, tt := range []struct{ text, locale, voice string }{{"link", "en-US", "en-us"}, {"button", "en-US", "en-us"}, {"Menü", "de-DE", "de"}, {"Schalter", "de-DE", "de"}} {
		t.Run(tt.text, func(t *testing.T) {
			req := validSpeechRequest()
			req.Text, req.Locale = tt.text, tt.locale
			got, err := (ESpeak{Command: cmd}).Synthesize(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			control, err := exec.CommandContext(t.Context(), cmd, "--stdout", "-v", tt.voice, "-s", "175", "-p", "50", "-a", "100", tt.text).Output()
			if err != nil {
				t.Fatal(err)
			}
			want, err := DecodeWAV(control)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.PCM) == 0 || !bytes.Equal(got.PCM, want.PCM) {
				t.Fatal("stdin input did not produce the full-word reference waveform")
			}
		})
	}
}
