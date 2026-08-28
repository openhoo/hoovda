package conformance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/openhoo/hoovda/internal/profile"
)

const SchemaVersion = 2

var requiredTags = []string{
	"speech", "braille", "focus", "browse-mode", "focus-mode", "quick-navigation",
	"text-navigation", "forms", "tables", "live-region", "dynamic-content",
	"browser-chrome", "en-US", "de-DE", "desktop", "laptop",
}

const (
	releaseCommit    = "5d92106f17e461dac62aa48257bbbf4183e033d0"
	captureToolURL   = "https://github.com/nvaccess/nvda/blob/5d92106f17e461dac62aa48257bbbf4183e033d0/tests/system/libraries/SystemTestSpy/speechSpyGlobalPlugin.py"
	captureToolHash  = "1bf6319b2b66896618b34d492b5772846ab55b150613935f4290803945087641"
	localizationPath = "source/locale/de/LC_MESSAGES/nvda.po"
	localizationHash = "f0a8360d9a6723a39f3bafacf56f0d105ea6355c8e1b9eac079fefac3b7e45f8"
)

var officialReferenceHashes = map[string]string{
	"tests/system/robot/chromeTests.py": "09988fded0f68cbfa115a4f23fff24073c2b33bcc0a7bab5ec97ac43e231b077",
	"source/NVDAHelper/__init__.py":     "a9b967a9d0377371dbc83ad5400d5c59f33dfd5b031f1edc9c0249de59467638",
}

type Manifest struct {
	Schema        string `json:"$schema,omitempty"`
	SchemaVersion int    `json:"schemaVersion"`
	Profile       string `json:"profile"`
	Status        string `json:"status"`
	Oracle        Oracle `json:"oracle"`
	Cases         []Case `json:"cases"`
	Notes         string `json:"notes,omitempty"`
}

type Oracle struct {
	Product         string `json:"product"`
	Version         string `json:"version"`
	Platform        string `json:"platform"`
	ReleaseTag      string `json:"releaseTag"`
	ReleaseCommit   string `json:"releaseCommit"`
	CaptureToolURL  string `json:"captureToolUrl"`
	CaptureToolHash string `json:"captureToolHash"`
	EvidenceLicense string `json:"evidenceLicense"`
}

type Case struct {
	ID                   string        `json:"id"`
	Locale               string        `json:"locale"`
	KeyboardLayout       string        `json:"keyboardLayout"`
	Fixture              string        `json:"fixture"`
	FixtureSHA256        string        `json:"fixtureSha256"`
	Expected             string        `json:"expected"`
	ExpectedSHA256       string        `json:"expectedSha256"`
	Observed             string        `json:"observed"`
	ObservedSHA256       string        `json:"observedSha256"`
	Reference            Reference     `json:"reference"`
	AdditionalReferences []Reference   `json:"additionalReferences,omitempty"`
	Localization         *Localization `json:"localization,omitempty"`
	Tags                 []string      `json:"tags"`
}

type Localization struct {
	URL      string `json:"url"`
	Revision string `json:"revision"`
	Path     string `json:"path"`
	SHA256   string `json:"sha256"`
	Locale   string `json:"locale"`
}

type Reference struct {
	URL       string `json:"url"`
	Revision  string `json:"revision"`
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	Test      string `json:"test"`
	Assertion string `json:"assertion"`
}

type Trace struct {
	SchemaVersion  int         `json:"schemaVersion"`
	CaseID         string      `json:"caseId"`
	Source         string      `json:"source"`
	Profile        string      `json:"profile"`
	Locale         string      `json:"locale"`
	KeyboardLayout string      `json:"keyboardLayout"`
	Steps          []TraceStep `json:"steps"`
}

type TraceStep struct {
	Command string          `json:"command"`
	Speech  []string        `json:"speech"`
	Braille []string        `json:"braille"`
	Mode    string          `json:"mode,omitempty"`
	Raw     json.RawMessage `json:"raw,omitempty"`
}

type Report struct {
	Manifest string   `json:"manifest"`
	Profile  string   `json:"profile"`
	Status   string   `json:"status"`
	Cases    int      `json:"cases"`
	Issues   []string `json:"issues"`
}

func Check(manifestPath string) (Report, error) {
	report := Report{Manifest: manifestPath, Status: "failed"}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return report, err
	}
	var manifest Manifest
	if err := decodeStrict(data, &manifest); err != nil {
		return report, fmt.Errorf("decode manifest: %w", err)
	}
	report.Profile, report.Cases = manifest.Profile, len(manifest.Cases)
	if manifest.Schema != "../schemas/manifest.schema.json" {
		report.Issues = append(report.Issues, "manifest $schema must pin ../schemas/manifest.schema.json")
	}
	if manifest.SchemaVersion != SchemaVersion {
		report.Issues = append(report.Issues, fmt.Sprintf("schemaVersion must equal %d", SchemaVersion))
	}
	if manifest.Profile != "nvda-web-2026.1.1" {
		report.Issues = append(report.Issues, "profile must equal nvda-web-2026.1.1")
	}
	if manifest.Status != "complete" {
		report.Issues = append(report.Issues, "oracle corpus status is not complete")
	}
	if manifest.Oracle.Product != "NVDA" || manifest.Oracle.Version != "2026.1.1" {
		report.Issues = append(report.Issues, "oracle product and version must be NVDA 2026.1.1")
	}
	if manifest.Oracle.Platform == "" || manifest.Oracle.ReleaseTag != "release-2026.1.1" || manifest.Oracle.ReleaseCommit != releaseCommit {
		report.Issues = append(report.Issues, "oracle platform, releaseTag, and releaseCommit must pin NVDA 2026.1.1")
	}
	if manifest.Oracle.CaptureToolURL != captureToolURL || manifest.Oracle.CaptureToolHash != captureToolHash || manifest.Oracle.EvidenceLicense != "GPL-2.0-or-later" {
		report.Issues = append(report.Issues, "oracle captureToolUrl, captureToolHash, and evidenceLicense are required")
	}
	root := filepath.Dir(manifestPath)
	seenCases := map[string]bool{}
	seenTags := map[string]bool{}
	seenMatrices := map[string]bool{}
	allowedTags := map[string]bool{}
	for _, tag := range requiredTags {
		allowedTags[tag] = true
	}
	for _, item := range manifest.Cases {
		if item.ID == "" || seenCases[item.ID] {
			report.Issues = append(report.Issues, fmt.Sprintf("case id %q is empty or duplicated", item.ID))
			continue
		}
		seenCases[item.ID] = true
		if item.Locale != "en-US" && item.Locale != "de-DE" {
			report.Issues = append(report.Issues, item.ID+": invalid locale")
		}
		if item.KeyboardLayout != "desktop" && item.KeyboardLayout != "laptop" {
			report.Issues = append(report.Issues, item.ID+": invalid keyboardLayout")
		}
		seenMatrices[item.Locale+"/"+item.KeyboardLayout] = true
		if issue := validateReference(item.Reference, manifest.Oracle.ReleaseCommit); issue != "" {
			report.Issues = append(report.Issues, item.ID+" reference: "+issue)
		}
		seenReferences := map[string]bool{item.Reference.Path + "\x00" + item.Reference.Test: true}
		for index, reference := range item.AdditionalReferences {
			if issue := validateReference(reference, manifest.Oracle.ReleaseCommit); issue != "" {
				report.Issues = append(report.Issues, fmt.Sprintf("%s additional reference %d: %s", item.ID, index+1, issue))
			}
			key := reference.Path + "\x00" + reference.Test
			if seenReferences[key] {
				report.Issues = append(report.Issues, fmt.Sprintf("%s additional reference %d is duplicated", item.ID, index+1))
			}
			seenReferences[key] = true
		}
		if issue := validateLocalization(item.Localization, item.Locale, manifest.Oracle.ReleaseCommit); issue != "" {
			report.Issues = append(report.Issues, item.ID+" localization: "+issue)
		}
		caseTags := map[string]bool{}
		for _, tag := range item.Tags {
			if !allowedTags[tag] {
				report.Issues = append(report.Issues, item.ID+" has unknown coverage tag: "+tag)
				continue
			}
			if caseTags[tag] {
				report.Issues = append(report.Issues, item.ID+" has duplicate coverage tag: "+tag)
				continue
			}
			caseTags[tag] = true
			if issue := validateCoverageEvidence(tag, append([]Reference{item.Reference}, item.AdditionalReferences...)); issue != "" {
				report.Issues = append(report.Issues, item.ID+" tag "+tag+": "+issue)
				continue
			}
			seenTags[tag] = true
		}
		if err := verifyFile(root, item.Fixture, item.FixtureSHA256, nil); err != nil {
			report.Issues = append(report.Issues, item.ID+" fixture: "+err.Error())
		}
		var expected Trace
		if err := verifyFile(root, item.Expected, item.ExpectedSHA256, &expected); err != nil {
			report.Issues = append(report.Issues, item.ID+" expected: "+err.Error())
		} else if issue := validateTrace(expected, item, manifest.Profile, "NVDA"); issue != "" {
			report.Issues = append(report.Issues, item.ID+" expected: "+issue)
		}
		var observed Trace
		if err := verifyFile(root, item.Observed, item.ObservedSHA256, &observed); err != nil {
			report.Issues = append(report.Issues, item.ID+" observed: "+err.Error())
		} else if issue := validateTrace(observed, item, manifest.Profile, "HooVDA"); issue != "" {
			report.Issues = append(report.Issues, item.ID+" observed: "+issue)
		}
		if len(expected.Steps) > 0 && len(observed.Steps) > 0 {
			if issue := compareSteps(expected.Steps, observed.Steps); issue != "" {
				report.Issues = append(report.Issues, item.ID+": "+issue)
			}
		}
	}
	for _, tag := range requiredTags {
		if !seenTags[tag] {
			report.Issues = append(report.Issues, "required coverage tag missing: "+tag)
		}
	}
	for _, locale := range []string{"en-US", "de-DE"} {
		for _, layout := range []string{"desktop", "laptop"} {
			matrix := locale + "/" + layout
			if !seenMatrices[matrix] {
				report.Issues = append(report.Issues, "required locale/layout matrix missing: "+matrix)
			}
		}
	}
	slices.Sort(report.Issues)
	if len(report.Issues) > 0 {
		return report, errors.New("conformance gate failed")
	}
	report.Status = "passed"
	return report, nil
}

func verifyFile(root, relative, expectedDigest string, decoded any) error {
	if !filepath.IsLocal(relative) {
		return errors.New("path must be local to corpus")
	}
	rootPath, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	rootPath, err = filepath.EvalSymlinks(rootPath)
	if err != nil {
		return err
	}
	path, err := filepath.Abs(filepath.Join(rootPath, relative))
	if err != nil {
		return err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	relativePath, err := filepath.Rel(rootPath, path)
	if err != nil || !filepath.IsLocal(relativePath) {
		return errors.New("resolved path escapes corpus")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(data)
	if !validDigest(expectedDigest) || hex.EncodeToString(digest[:]) != expectedDigest {
		return errors.New("SHA-256 mismatch")
	}
	if decoded != nil {
		if err := decodeStrict(data, decoded); err != nil {
			return fmt.Errorf("decode JSON: %w", err)
		}
	}
	return nil
}

func decodeStrict(data []byte, decoded any) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(decoded); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err != nil {
			return err
		}
		return errors.New("trailing JSON value")
	}
	return nil
}

func validateTrace(trace Trace, item Case, profileName, source string) string {
	if trace.SchemaVersion != SchemaVersion || trace.CaseID != item.ID || trace.Profile != profileName || trace.Locale != item.Locale || trace.KeyboardLayout != item.KeyboardLayout {
		return "trace identity does not match manifest"
	}
	if trace.Source != source {
		return fmt.Sprintf("source must equal %s", source)
	}
	if len(trace.Steps) == 0 {
		return "trace has no steps"
	}
	hasSpeech, hasBraille := false, false
	for index, step := range trace.Steps {
		if strings.TrimSpace(step.Command) == "" {
			return fmt.Sprintf("step %d has no command", index+1)
		}
		if step.Mode != "browse" && step.Mode != "focus" {
			return fmt.Sprintf("step %d mode must equal browse or focus", index+1)
		}
		hasSpeech = hasSpeech || len(step.Speech) > 0
		hasBraille = hasBraille || len(step.Braille) > 0
		if issue := validateStepEvidence(step, item, source); issue != "" {
			return fmt.Sprintf("step %d %s", index+1, issue)
		}
	}
	if slices.Contains(item.Tags, "speech") && !hasSpeech {
		return "trace tagged speech has no speech evidence"
	}
	if slices.Contains(item.Tags, "braille") && !hasBraille {
		return "trace tagged braille has no braille evidence"
	}
	return ""
}

func validateStepEvidence(step TraceStep, item Case, source string) string {
	if len(step.Raw) == 0 || string(step.Raw) == "null" {
		return "has no raw evidence"
	}
	var evidence map[string]any
	if err := json.Unmarshal(step.Raw, &evidence); err != nil || evidence == nil {
		return "raw evidence must be an object"
	}
	gesture, ok := evidence["gesture"].(string)
	if !ok || strings.TrimSpace(gesture) == "" {
		return "raw evidence has no gesture"
	}
	if source == "NVDA" {
		upstreamTest, ok := evidence["upstreamTest"].(string)
		if !ok || strings.TrimSpace(upstreamTest) == "" {
			return "raw NVDA evidence has no upstreamTest"
		}
		declared := item.Reference.Test == upstreamTest
		for _, reference := range item.AdditionalReferences {
			declared = declared || reference.Test == upstreamTest
		}
		if !declared {
			return "raw NVDA upstreamTest is not declared by the case references"
		}
		return ""
	}
	if capture, ok := evidence["capture"].(string); !ok || capture != "linux-container-chromium-at-spi" {
		return "raw HooVDA evidence has invalid capture boundary"
	}
	command, ok := profile.CommandByID(step.Command)
	if !ok {
		return "uses an unknown HooVDA command"
	}
	gestures := command.Desktop
	if item.KeyboardLayout == "laptop" {
		gestures = command.Laptop
	}
	normalized := profile.NormalizeGesture(gesture)
	for _, candidate := range gestures {
		if profile.NormalizeGesture(candidate) == normalized {
			return ""
		}
	}
	return fmt.Sprintf("gesture %q does not deliver command %q for %s layout", gesture, step.Command, item.KeyboardLayout)
}

func validateReference(reference Reference, releaseCommit string) string {
	wantURL := "https://github.com/nvaccess/nvda/blob/" + releaseCommit + "/" + reference.Path
	if reference.URL != wantURL {
		return "url must pin the exact NVDA release commit and source path"
	}
	if reference.Revision != releaseCommit || !validRevision(reference.Revision) {
		return "revision must equal oracle releaseCommit"
	}
	if !filepath.IsLocal(reference.Path) || strings.TrimSpace(reference.Path) == "" {
		return "path must be a non-empty local repository path"
	}
	wantHash, known := officialReferenceHashes[reference.Path]
	if !known || reference.SHA256 != wantHash {
		return "path and sha256 must match the audited official source registry"
	}
	if strings.TrimSpace(reference.Test) == "" || strings.TrimSpace(reference.Assertion) == "" {
		return "test and assertion identities are required"
	}
	return ""
}

func validateCoverageEvidence(tag string, references []Reference) string {
	matches := func(path, test string) bool {
		for _, reference := range references {
			if reference.Path == path && reference.Test == test {
				return true
			}
		}
		return false
	}
	switch tag {
	case "focus", "focus-mode", "forms":
		if !matches("tests/system/robot/chromeTests.py", "test_aria_details_noVBufNoTextInterface") {
			return "requires test_aria_details_noVBufNoTextInterface"
		}
	case "browse-mode":
		if !matches("tests/system/robot/chromeTests.py", "test_quickNavTargetReporting") && !matches("tests/system/robot/chromeTests.py", "test_tableInStyleDisplayTable") {
			return "requires a pinned Chrome browse-mode navigation assertion"
		}
	case "quick-navigation":
		if !matches("tests/system/robot/chromeTests.py", "test_quickNavTargetReporting") && !matches("tests/system/robot/chromeTests.py", "test_tableInStyleDisplayTable") {
			return "requires a pinned Chrome quick-navigation assertion"
		}
	case "text-navigation":
		if !matches("tests/system/robot/chromeTests.py", "test_textParagraphNavigation") {
			return "requires test_textParagraphNavigation"
		}
	case "tables":
		if !matches("tests/system/robot/chromeTests.py", "test_tableInStyleDisplayTable") {
			return "requires test_tableInStyleDisplayTable"
		}
	case "live-region", "dynamic-content":
		if !matches("source/NVDAHelper/__init__.py", "nvdaControllerInternal_reportLiveRegion") {
			return "requires nvdaControllerInternal_reportLiveRegion"
		}
	}
	return ""
}

func validateLocalization(localization *Localization, locale, releaseCommit string) string {
	if locale == "en-US" {
		if localization != nil {
			return "en-US cases must not declare a localization catalog"
		}
		return ""
	}
	if localization == nil {
		return "de-DE cases require the pinned official localization catalog"
	}
	wantURL := "https://github.com/nvaccess/nvda/blob/" + releaseCommit + "/" + localizationPath
	if localization.URL != wantURL || localization.Revision != releaseCommit || localization.Path != localizationPath || localization.SHA256 != localizationHash || localization.Locale != "de-DE" {
		return "catalog identity must match the audited official de-DE source"
	}
	return ""
}

func compareSteps(expected, observed []TraceStep) string {
	if len(expected) != len(observed) {
		return fmt.Sprintf("step count mismatch: expected %d, observed %d", len(expected), len(observed))
	}
	for index := range expected {
		want, got := expected[index], observed[index]
		if want.Command != got.Command {
			return fmt.Sprintf("step %d command mismatch: expected %q, observed %q", index+1, want.Command, got.Command)
		}
		if !slices.Equal(want.Speech, got.Speech) {
			return fmt.Sprintf("step %d speech mismatch", index+1)
		}
		if !slices.Equal(want.Braille, got.Braille) {
			return fmt.Sprintf("step %d braille mismatch", index+1)
		}
		if want.Mode != got.Mode {
			return fmt.Sprintf("step %d mode mismatch: expected %q, observed %q", index+1, want.Mode, got.Mode)
		}
	}
	return ""
}

func validDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func validRevision(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 20 && value == strings.ToLower(value)
}
