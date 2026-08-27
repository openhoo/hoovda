package model

import (
	"fmt"
	"unicode/utf8"
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
