package braille

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"unicode/utf8"
)

const MaxInputBytes = 1 << 20

type Result struct {
	Text   string `json:"text"`
	Cells  []byte `json:"cells"`
	Cursor int    `json:"cursor"`
	Table  string `json:"table"`
}

type Translator interface {
	Translate(context.Context, string, string, int) (Result, error)
	Name() string
	Version(context.Context) string
}

type Liblouis struct {
	Command string
}

func (l Liblouis) Name() string { return "liblouis" }

func (l Liblouis) Version(ctx context.Context) string {
	output, err := exec.CommandContext(ctx, l.command(), "--version").CombinedOutput()
	if err != nil {
		return "unavailable"
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(output)), "\n")
	return line
}

func (l Liblouis) Translate(ctx context.Context, text, locale string, cursor int) (Result, error) {
	if len(text) > MaxInputBytes {
		return Result{}, errors.New("braille input exceeds limit")
	}
	if cursor < 0 || cursor > utf8.RuneCountInString(text) {
		return Result{}, errors.New("braille cursor outside text")
	}
	table, err := tableForLocale(locale)
	if err != nil {
		return Result{}, err
	}
	command := exec.CommandContext(ctx, l.command(), table)
	command.Stdin = strings.NewReader(text + "\n")
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		return Result{}, fmt.Errorf("liblouis translation failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	translated := strings.TrimSuffix(stdout.String(), "\n")
	return Result{Text: translated, Cells: cellsFromUnicode(translated), Cursor: cursor, Table: table}, nil
}

func (l Liblouis) command() string {
	if l.Command != "" {
		return l.Command
	}
	return "lou_translate"
}

type Passthrough struct{}

func (Passthrough) Name() string                   { return "passthrough-test-only" }
func (Passthrough) Version(context.Context) string { return "1" }
func (Passthrough) Translate(_ context.Context, text, locale string, cursor int) (Result, error) {
	if cursor < 0 || cursor > utf8.RuneCountInString(text) {
		return Result{}, errors.New("braille cursor outside text")
	}
	return Result{Text: text, Cells: []byte(text), Cursor: cursor, Table: "passthrough-" + locale}, nil
}

func tableForLocale(locale string) (string, error) {
	switch locale {
	case "en-US":
		return "en-us-g2.ctb", nil
	case "de-DE":
		return "de-g2.ctb", nil
	default:
		return "", fmt.Errorf("unsupported braille locale %q", locale)
	}
}

func cellsFromUnicode(value string) []byte {
	cells := make([]byte, 0, utf8.RuneCountInString(value))
	for _, r := range value {
		if r >= 0x2800 && r <= 0x28ff {
			cells = append(cells, byte(r-0x2800))
		} else if r <= 0xff {
			cells = append(cells, byte(r))
		} else {
			cells = append(cells, 0)
		}
	}
	return cells
}
