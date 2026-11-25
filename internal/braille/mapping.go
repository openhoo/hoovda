package braille

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/openhoo/hoovda/internal/processoutput"
)

// Mapping records both directions of a single contracted translation. Values
// are zero-based indexes, not UTF-8 byte offsets. A contraction may map many
// input runes to one cell; a capital or number prefix may add extra cells.
type Mapping struct {
	Cells      []byte
	CellToRune []int
	RuneToCell []int
}

// Mapper is optional. A translator without it cannot route contracted cells
// reliably; callers must not substitute a text offset for a cell number.
type Mapper interface {
	TranslateMapped(context.Context, string, string) (Mapping, error)
}

const maxMappedCells = 2 << 20

func (l Liblouis) TranslateMapped(ctx context.Context, text, locale string) (Mapping, error) {
	if err := ctx.Err(); err != nil {
		return Mapping{}, err
	}
	if len(text) > MaxInputBytes || !utf8.ValidString(text) {
		return Mapping{}, errors.New("invalid or oversized braille input")
	}
	table, err := tableForLocale(locale)
	if err != nil {
		return Mapping{}, err
	}
	n := utf8.RuneCountInString(text)
	if n == 0 {
		return Mapping{}, nil
	}
	// Binary transport avoids CLI escape parsing and carries Unicode code points
	// without relying on the subprocess locale. The helper handles UCS-2 builds.
	input := make([]byte, 4, 4+4*n)
	binary.LittleEndian.PutUint32(input, uint32(n))
	for _, r := range text {
		input = binary.LittleEndian.AppendUint32(input, uint32(r))
	}
	name := l.MapCommand
	if name == "" {
		name = "hoovda-braille-map"
		if filepath.IsAbs(l.Command) {
			name = filepath.Join(filepath.Dir(l.Command), name)
		}
	}
	cmd := exec.CommandContext(ctx, name, table)
	cmd.Stdin = bytes.NewReader(input)
	stdout := processoutput.Buffer{Limit: 8 + 5*maxMappedCells + 4*n}
	stderr := processoutput.Buffer{Limit: 4096}
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := errors.Join(cmd.Run(), ctx.Err(), stdout.Err, stderr.Err); err != nil {
		return Mapping{}, fmt.Errorf("liblouis position mapping failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return decodeMapping(stdout.Bytes(), n)
}

func decodeMapping(buf []byte, n int) (Mapping, error) {
	if len(buf) < 8 || n <= 0 || n > MaxInputBytes {
		return Mapping{}, errors.New("invalid braille mapping header")
	}
	cells := uint64(binary.LittleEndian.Uint32(buf))
	runes := uint64(binary.LittleEndian.Uint32(buf[4:]))
	if cells == 0 || cells > maxMappedCells || runes != uint64(n) || uint64(len(buf)) != 8+5*cells+4*runes {
		return Mapping{}, errors.New("invalid braille mapping dimensions")
	}
	k := int(cells)
	m := Mapping{Cells: append([]byte(nil), buf[8:8+k]...), CellToRune: make([]int, k), RuneToCell: make([]int, n)}
	buf = buf[8+k:]
	for _, a := range []struct {
		idx   []int
		bound int
	}{{m.CellToRune, n}, {m.RuneToCell, k}} {
		for i := range a.idx {
			v := binary.LittleEndian.Uint32(buf)
			buf = buf[4:]
			if uint64(v) >= uint64(a.bound) {
				return Mapping{}, errors.New("braille mapping index outside range")
			}
			a.idx[i] = int(v)
		}
	}
	return m, nil
}

func (Passthrough) TranslateMapped(ctx context.Context, text, locale string) (Mapping, error) {
	if err := ctx.Err(); err != nil {
		return Mapping{}, err
	}
	if len(text) > MaxInputBytes || !utf8.ValidString(text) {
		return Mapping{}, errors.New("invalid or oversized braille input")
	}
	if _, err := tableForLocale(locale); err != nil {
		return Mapping{}, err
	}
	m := Mapping{Cells: cellsFromUnicode(text)}
	n := len(m.Cells)
	m.CellToRune = make([]int, n)
	m.RuneToCell = make([]int, n)
	for i := 0; i < n; i++ {
		m.CellToRune[i], m.RuneToCell[i] = i, i
	}
	return m, nil
}
