package synth

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

type ESpeak struct {
	Command string
}

func (e ESpeak) Name() string { return "espeak-ng" }

func (e ESpeak) Version(ctx context.Context) string {
	output, err := exec.CommandContext(ctx, e.command(), "--version").CombinedOutput()
	if err != nil {
		return "unavailable"
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(output)), "\n")
	return line
}

func (e ESpeak) Synthesize(ctx context.Context, request Request) (Audio, error) {
	if err := request.Validate(); err != nil {
		return Audio{}, err
	}
	voice := request.Voice
	if voice == "" {
		if request.Locale == "de-DE" {
			voice = "de"
		} else {
			voice = "en-us"
		}
	}
	command := exec.CommandContext(ctx, e.command(),
		"--stdout", "--stdin", "-v", voice,
		"-s", strconv.Itoa(request.Rate),
		"-p", strconv.Itoa(request.Pitch),
		"-a", strconv.Itoa(request.Volume),
	)
	command.Stdin = strings.NewReader(request.Text)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		return Audio{}, fmt.Errorf("espeak synthesis failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	audio, err := DecodeWAV(stdout.Bytes())
	if err != nil {
		return Audio{}, fmt.Errorf("decode espeak WAV: %w", err)
	}
	return audio, nil
}

func (e ESpeak) command() string {
	if e.Command != "" {
		return e.Command
	}
	return "espeak-ng"
}
