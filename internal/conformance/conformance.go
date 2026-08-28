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
)

const SchemaVersion = 2

var requiredTags = []string{
	"speech", "braille", "focus", "browse-mode", "focus-mode", "quick-navigation",
	"text-navigation", "forms", "tables", "live-region", "dynamic-content",
	"browser-chrome", "en-US", "de-DE", "desktop", "laptop",
}

const (
	releaseCommit   = "5d92106f17e461dac62aa48257bbbf4183e033d0"
	captureToolURL  = "https://github.com/nvaccess/nvda/blob/5d92106f17e461dac62aa48257bbbf4183e033d0/tests/system/libraries/SystemTestSpy/speechSpyGlobalPlugin.py"
	captureToolHash = "1bf6319b2b66896618b34d492b5772846ab55b150613935f4290803945087641"
)

var officialReferenceHashes = map[string]string{
	"tests/system/robot/chromeTests.py": "09988fded0f68cbfa115a4f23fff24073c2b33bcc0a7bab5ec97ac43e231b077",
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
	ID             string    `json:"id"`
	Locale         string    `json:"locale"`
	KeyboardLayout string    `json:"keyboardLayout"`
	Fixture        string    `json:"fixture"`
	FixtureSHA256  string    `json:"fixtureSha256"`
	Expected       string    `json:"expected"`
	ExpectedSHA256 string    `json:"expectedSha256"`
	Observed       string    `json:"observed"`
	ObservedSHA256 string    `json:"observedSha256"`
	Reference      Reference `json:"reference"`
	Tags           []string  `json:"tags"`
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
		for _, tag := range item.Tags {
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

func validateTrace(trace Trace, item Case, profile, source string) string {
	if trace.SchemaVersion != SchemaVersion || trace.CaseID != item.ID || trace.Profile != profile || trace.Locale != item.Locale || trace.KeyboardLayout != item.KeyboardLayout {
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
	}
	if !hasSpeech || !hasBraille {
		return "trace must contain both speech and braille evidence"
	}
	return ""
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
