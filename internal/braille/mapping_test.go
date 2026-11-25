package braille

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"slices"
	"testing"
)

func TestMappingRejectsCorruptHelperOutput(t *testing.T) {
	// One cell associated with two input runes (a contraction).
	valid := []byte{1, 0, 0, 0, 2, 0, 0, 0, 46, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	m, err := decodeMapping(valid, 2)
	if err != nil || !slices.Equal(m.RuneToCell, []int{0, 0}) {
		t.Fatalf("valid contraction: %+v %v", m, err)
	}
	for _, tt := range []struct {
		name string
		data []byte
	}{{"truncated", valid[:20]}, {"extra", append(append([]byte{}, valid...), 0)}, {"cell-outside-input", func() []byte { b := bytes.Clone(valid); binary.LittleEndian.PutUint32(b[9:], 2); return b }()}, {"rune-outside-output", func() []byte { b := bytes.Clone(valid); binary.LittleEndian.PutUint32(b[13:], 1); return b }()}} {
		if _, err := decodeMapping(tt.data, 2); err == nil {
			t.Errorf("%s accepted", tt.name)
		}
	}
}
func TestMappedPassthroughUsesRunePositions(t *testing.T) {
	m, err := (Passthrough{}).TranslateMapped(context.Background(), "Aé⠃🌈", "en-US")
	if err != nil || !slices.Equal(m.Cells, []byte{65, 233, 3, 0}) || !slices.Equal(m.CellToRune, []int{0, 1, 2, 3}) || !slices.Equal(m.RuneToCell, m.CellToRune) {
		t.Fatalf("mapped passthrough = %+v %v", m, err)
	}
}
func TestMappedLocaleAndInputAreCheckedWithoutHelper(t *testing.T) {
	t.Parallel()
	l := Liblouis{MapCommand: "does-not-exist"}
	for _, tt := range []struct{ s, loc string }{{"test", "fr-FR"}, {string([]byte{255}), "en-US"}} {
		if _, err := l.TranslateMapped(t.Context(), tt.s, tt.loc); err == nil {
			t.Fatal("bad input accepted")
		}
	}
	if m, err := l.TranslateMapped(t.Context(), "", "de-DE"); err != nil || len(m.Cells) != 0 {
		t.Fatalf("empty mapping: %+v %v", m, err)
	}
}
func TestPinnedLiblouisMapping(t *testing.T) {
	cmd := os.Getenv("HOOVDA_TEST_BRAILLE_MAP")
	if cmd == "" {
		var err error
		cmd, err = exec.LookPath("hoovda-braille-map")
		if err != nil {
			t.Skip("native helper unavailable")
		}
	}
	l := Liblouis{MapCommand: cmd}
	for _, tt := range []struct {
		text, locale   string
		cells          []byte
		toRune, toCell []int
	}{
		{"the cat", "en-US", []byte{46, 0, 9, 1, 30}, []int{0, 3, 4, 5, 6}, []int{0, 0, 0, 1, 2, 3, 4}},
		{"The cat", "en-US", []byte{32, 46, 0, 9, 1, 30}, []int{0, 0, 3, 4, 5, 6}, []int{0, 0, 0, 2, 3, 4, 5}},
		{"12", "en-US", []byte{60, 1, 3}, []int{0, 0, 1}, []int{0, 2}},
		{"der Hund", "de-DE", []byte{23, 0, 19, 50, 25}, []int{0, 3, 4, 5, 7}, []int{0, 0, 0, 1, 2, 3, 3, 4}},
		{"Über 42", "de-DE", []byte{51, 0, 60, 25, 3}, []int{0, 4, 5, 5, 6}, []int{0, 0, 0, 0, 1, 2, 4}},
	} {
		t.Run(tt.text, func(t *testing.T) {
			m, err := l.TranslateMapped(t.Context(), tt.text, tt.locale)
			if err != nil || !slices.Equal(m.Cells, tt.cells) || !slices.Equal(m.CellToRune, tt.toRune) || !slices.Equal(m.RuneToCell, tt.toCell) {
				t.Fatalf("real translation: %+v %v", m, err)
			}
		})
	}
	m, err := l.TranslateMapped(t.Context(), "🌈 x", "en-US")
	if err != nil || len(m.RuneToCell) != 3 || m.CellToRune[m.RuneToCell[1]] != 1 || m.CellToRune[m.RuneToCell[2]] != 2 {
		t.Fatalf("UTF-16 surrogate pair shifted Go rune indexes: %+v %v", m, err)
	}
}
