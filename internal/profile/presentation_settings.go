package profile

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

type SpeechSymbolLevel string

const (
	SpeechSymbolsNone      SpeechSymbolLevel = "none"
	SpeechSymbolsSome      SpeechSymbolLevel = "some"
	SpeechSymbolsMost      SpeechSymbolLevel = "most"
	SpeechSymbolsAll       SpeechSymbolLevel = "all"
	SpeechSymbolsCharacter SpeechSymbolLevel = "character"
)

type BrailleTether string

const (
	BrailleTetherAuto   BrailleTether = "auto"
	BrailleTetherFocus  BrailleTether = "focus"
	BrailleTetherReview BrailleTether = "review"
)

type FontAttributeReporting string

const (
	FontAttributesOff              FontAttributeReporting = "off"
	FontAttributesSpeech           FontAttributeReporting = "speech"
	FontAttributesBraille          FontAttributeReporting = "braille"
	FontAttributesSpeechAndBraille FontAttributeReporting = "speechAndBraille"
)

type TableHeaderReporting string

const (
	TableHeadersOff            TableHeaderReporting = "off"
	TableHeadersRowsAndColumns TableHeaderReporting = "rowsAndColumns"
	TableHeadersRows           TableHeaderReporting = "rows"
	TableHeadersColumns        TableHeaderReporting = "columns"
)

const (
	SpellingErrorsSpeech  = "speech"
	SpellingErrorsSound   = "sound"
	SpellingErrorsBraille = "braille"
)

// PresentationSettings is the web-relevant NVDA presentation surface exposed
// by protocol v2. The JSON shape is deliberately exact: missing and unknown
// fields are rejected so a test cannot silently run with adapter defaults.
type PresentationSettings struct {
	SpeechSymbolLevel               SpeechSymbolLevel      `json:"speechSymbolLevel"`
	BrailleTether                   BrailleTether          `json:"brailleTether"`
	ReportKeyboardShortcuts         bool                   `json:"reportKeyboardShortcuts"`
	ReportObjectPositionInformation bool                   `json:"reportObjectPositionInformation"`
	ReportObjectDescriptions        bool                   `json:"reportObjectDescriptions"`
	ReportDynamicContentChanges     bool                   `json:"reportDynamicContentChanges"`
	ReportAriaDescription           bool                   `json:"reportAriaDescription"`
	ReportDetails                   bool                   `json:"reportDetails"`
	ReportFontName                  bool                   `json:"reportFontName"`
	ReportFontSize                  bool                   `json:"reportFontSize"`
	FontAttributeReporting          FontAttributeReporting `json:"fontAttributeReporting"`
	ReportColor                     bool                   `json:"reportColor"`
	ReportStyle                     bool                   `json:"reportStyle"`
	ReportSpellingErrors            []string               `json:"reportSpellingErrors"`
	ReportTables                    bool                   `json:"reportTables"`
	IncludeLayoutTables             bool                   `json:"includeLayoutTables"`
	ReportTableHeaders              TableHeaderReporting   `json:"reportTableHeaders"`
	ReportTableCellCoordinates      bool                   `json:"reportTableCellCoordinates"`
	ReportLinks                     bool                   `json:"reportLinks"`
	ReportLinkType                  bool                   `json:"reportLinkType"`
	ReportGraphics                  bool                   `json:"reportGraphics"`
	ReportComments                  bool                   `json:"reportComments"`
	ReportBookmarks                 bool                   `json:"reportBookmarks"`
	ReportLists                     bool                   `json:"reportLists"`
	ReportHeadings                  bool                   `json:"reportHeadings"`
	ReportBlockQuotes               bool                   `json:"reportBlockQuotes"`
	ReportGroupings                 bool                   `json:"reportGroupings"`
	ReportLandmarks                 bool                   `json:"reportLandmarks"`
	ReportArticles                  bool                   `json:"reportArticles"`
	ReportFrames                    bool                   `json:"reportFrames"`
	ReportFigures                   bool                   `json:"reportFigures"`
	ReportClickable                 bool                   `json:"reportClickable"`
}

var presentationSettingKeys = map[string]struct{}{
	"speechSymbolLevel": {}, "brailleTether": {},
	"reportKeyboardShortcuts": {}, "reportObjectPositionInformation": {},
	"reportObjectDescriptions": {}, "reportDynamicContentChanges": {},
	"reportAriaDescription": {}, "reportDetails": {}, "reportFontName": {},
	"reportFontSize": {}, "fontAttributeReporting": {}, "reportColor": {},
	"reportStyle": {}, "reportSpellingErrors": {}, "reportTables": {},
	"includeLayoutTables": {}, "reportTableHeaders": {},
	"reportTableCellCoordinates": {}, "reportLinks": {}, "reportLinkType": {},
	"reportGraphics": {}, "reportComments": {}, "reportBookmarks": {},
	"reportLists": {}, "reportHeadings": {}, "reportBlockQuotes": {},
	"reportGroupings": {}, "reportLandmarks": {}, "reportArticles": {},
	"reportFrames": {}, "reportFigures": {}, "reportClickable": {},
}

func DefaultPresentationSettings() PresentationSettings {
	return PresentationSettings{
		SpeechSymbolLevel: SpeechSymbolsSome, BrailleTether: BrailleTetherAuto,
		ReportKeyboardShortcuts: true, ReportObjectPositionInformation: true,
		ReportObjectDescriptions: true, ReportDynamicContentChanges: true,
		ReportAriaDescription: true, ReportDetails: true,
		FontAttributeReporting: FontAttributesOff,
		ReportSpellingErrors:   []string{SpellingErrorsSpeech},
		ReportTables:           true, ReportTableHeaders: TableHeadersRowsAndColumns,
		ReportTableCellCoordinates: true, ReportLinks: true, ReportLinkType: true,
		ReportGraphics: true, ReportComments: true, ReportBookmarks: true,
		ReportLists: true, ReportHeadings: true, ReportBlockQuotes: true,
		ReportGroupings: true, ReportLandmarks: true, ReportFrames: true,
		ReportFigures: true, ReportClickable: true,
	}
}

func (s PresentationSettings) Clone() PresentationSettings {
	s.ReportSpellingErrors = slices.Clone(s.ReportSpellingErrors)
	return s
}

func (s PresentationSettings) Validate() error {
	if !slices.Contains([]SpeechSymbolLevel{SpeechSymbolsNone, SpeechSymbolsSome, SpeechSymbolsMost, SpeechSymbolsAll, SpeechSymbolsCharacter}, s.SpeechSymbolLevel) {
		return errors.New("speechSymbolLevel is invalid")
	}
	if !slices.Contains([]BrailleTether{BrailleTetherAuto, BrailleTetherFocus, BrailleTetherReview}, s.BrailleTether) {
		return errors.New("brailleTether is invalid")
	}
	if !slices.Contains([]FontAttributeReporting{FontAttributesOff, FontAttributesSpeech, FontAttributesBraille, FontAttributesSpeechAndBraille}, s.FontAttributeReporting) {
		return errors.New("fontAttributeReporting is invalid")
	}
	if !slices.Contains([]TableHeaderReporting{TableHeadersOff, TableHeadersRowsAndColumns, TableHeadersRows, TableHeadersColumns}, s.ReportTableHeaders) {
		return errors.New("reportTableHeaders is invalid")
	}
	seen := make(map[string]bool, len(s.ReportSpellingErrors))
	for _, channel := range s.ReportSpellingErrors {
		if channel != SpellingErrorsSpeech && channel != SpellingErrorsSound && channel != SpellingErrorsBraille {
			return errors.New("reportSpellingErrors is invalid")
		}
		if seen[channel] {
			return errors.New("reportSpellingErrors contains duplicates")
		}
		seen[channel] = true
	}
	return nil
}

func (s *PresentationSettings) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if len(fields) != len(presentationSettingKeys) {
		return fmt.Errorf("presentation settings must contain exactly %d fields", len(presentationSettingKeys))
	}
	for key := range fields {
		if _, ok := presentationSettingKeys[key]; !ok {
			return fmt.Errorf("unknown presentation setting %q", key)
		}
	}
	type plain PresentationSettings
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	value := PresentationSettings(decoded)
	if err := value.Validate(); err != nil {
		return err
	}
	*s = value.Clone()
	return nil
}
