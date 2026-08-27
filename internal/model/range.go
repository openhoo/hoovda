package model

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

type TextUnit string

const (
	TextUnitCharacter TextUnit = "character"
	TextUnitWord      TextUnit = "word"
	TextUnitLine      TextUnit = "line"
)

type TextRange struct {
	Object ObjectID `json:"object"`
	Start  int      `json:"start"`
	End    int      `json:"end"`
}

func (r TextRange) Validate(text string) error {
	length := utf8.RuneCountInString(text)
	if r.Start < 0 || r.End < r.Start || r.End > length {
		return fmt.Errorf("invalid text range %d..%d for %d runes", r.Start, r.End, length)
	}
	return nil
}

func (r TextRange) Text(text string) (string, error) {
	if err := r.Validate(text); err != nil {
		return "", err
	}
	runes := []rune(text)
	return string(runes[r.Start:r.End]), nil
}

func CharacterRange(id ObjectID, text string, offset int) (TextRange, error) {
	length := utf8.RuneCountInString(text)
	if offset < 0 || offset >= length {
		return TextRange{}, fmt.Errorf("character offset %d outside 0..%d", offset, length)
	}
	return TextRange{Object: id, Start: offset, End: offset + 1}, nil
}

// TextUnitRanges returns deterministic Unicode-rune ranges. Word ranges follow
// AT-SPI word granularity: a word starts at a letter/number and extends to the
// next word start, including intervening punctuation and spacing. Line ranges
// exclude line terminators but preserve empty visual lines.
func TextUnitRanges(id ObjectID, text string, unit TextUnit) ([]TextRange, error) {
	runes := []rune(text)
	switch unit {
	case TextUnitCharacter:
		result := make([]TextRange, 0, len(runes))
		for offset := range runes {
			result = append(result, TextRange{Object: id, Start: offset, End: offset + 1})
		}
		return result, nil
	case TextUnitWord:
		starts := make([]int, 0)
		inWord := false
		for offset, character := range runes {
			word := unicode.IsLetter(character) || unicode.IsNumber(character)
			if word && !inWord {
				starts = append(starts, offset)
			}
			inWord = word
		}
		result := make([]TextRange, 0, len(starts))
		for index, start := range starts {
			end := len(runes)
			if index+1 < len(starts) {
				end = starts[index+1]
			}
			result = append(result, TextRange{Object: id, Start: start, End: end})
		}
		return result, nil
	case TextUnitLine:
		result := make([]TextRange, 0, strings.Count(text, "\n")+1)
		start := 0
		for offset, character := range runes {
			if character != '\n' {
				continue
			}
			end := offset
			if end > start && runes[end-1] == '\r' {
				end--
			}
			result = append(result, TextRange{Object: id, Start: start, End: end})
			start = offset + 1
		}
		result = append(result, TextRange{Object: id, Start: start, End: len(runes)})
		return result, nil
	default:
		return nil, fmt.Errorf("unsupported text unit %q", unit)
	}
}

// MoveTextUnit returns the nearest range strictly after or before offset.
// Cursor offsets are range starts, matching AT-SPI/NVDA caret semantics.
func MoveTextUnit(id ObjectID, text string, offset, direction int, unit TextUnit) (TextRange, bool, error) {
	ranges, err := TextUnitRanges(id, text, unit)
	if err != nil {
		return TextRange{}, false, err
	}
	if direction > 0 {
		for _, candidate := range ranges {
			if candidate.Start > offset {
				return candidate, true, nil
			}
		}
		return TextRange{}, false, nil
	}
	if direction < 0 {
		for index := len(ranges) - 1; index >= 0; index-- {
			if ranges[index].Start < offset {
				return ranges[index], true, nil
			}
		}
		return TextRange{}, false, nil
	}
	return TextRange{}, false, fmt.Errorf("text movement direction must be non-zero")
}
