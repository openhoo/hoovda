package conformance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIncompleteManifestFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	data := []byte(`{"schemaVersion":2,"profile":"nvda-web-2026.1.1","status":"incomplete","oracle":{"product":"NVDA","version":"2026.1.1","platform":"external","releaseTag":"release-2026.1.1","releaseCommit":"5d92106f17e461dac62aa48257bbbf4183e033d0","captureToolUrl":"https://github.com/nvaccess/nvda/blob/5d92106f17e461dac62aa48257bbbf4183e033d0/tests/system/libraries/SystemTestSpy/speechSpyGlobalPlugin.py","captureToolHash":"1bf6319b2b66896618b34d492b5772846ab55b150613935f4290803945087641","evidenceLicense":"GPL-2.0-or-later"},"cases":[]}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := Check(path)
	if err == nil || report.Status != "failed" || len(report.Issues) == 0 {
		t.Fatalf("report=%#v err=%v", report, err)
	}
}

func TestCompleteMatchingCorpusPasses(t *testing.T) {
	path := writeCorpus(t, false)
	report, err := Check(path)
	if err != nil || report.Status != "passed" || len(report.Issues) != 0 {
		t.Fatalf("report=%#v err=%v", report, err)
	}
}

func TestCompleteMismatchingCorpusFails(t *testing.T) {
	path := writeCorpus(t, true)
	report, err := Check(path)
	if err == nil || report.Status != "failed" {
		t.Fatalf("report=%#v err=%v", report, err)
	}
	if !slicesContainSubstring(report.Issues, "speech mismatch") {
		t.Fatalf("issues=%#v", report.Issues)
	}
}

func writeCorpus(t *testing.T, mismatch bool) string {
	t.Helper()
	root := t.TempDir()
	fixture := []byte("<!doctype html><h1>Checkout</h1>\n")
	expected := Trace{
		SchemaVersion: SchemaVersion, CaseID: "checkout", Source: "NVDA",
		Profile: "nvda-web-2026.1.1", Locale: "en-US", KeyboardLayout: "desktop",
		Steps: []TraceStep{{Command: "nextHeading", Speech: []string{"Checkout heading level 1"}, Braille: []string{"Checkout"}, Mode: "browse", Raw: json.RawMessage(`{"upstreamTest":"test_checkout","gesture":"h"}`)}},
	}
	observed := expected
	observed.Source = "HooVDA"
	observed.Steps = append([]TraceStep(nil), expected.Steps...)
	if mismatch {
		observed.Steps[0].Speech = []string{"Checkout heading"}
	}
	expectedData := marshalJSON(t, expected)
	observedData := marshalJSON(t, observed)
	writeFile(t, filepath.Join(root, "fixture.html"), fixture)
	writeFile(t, filepath.Join(root, "expected.json"), expectedData)
	writeFile(t, filepath.Join(root, "observed.json"), observedData)
	manifest := Manifest{
		Schema: "../schemas/manifest.schema.json", SchemaVersion: SchemaVersion,
		Profile: "nvda-web-2026.1.1", Status: "complete",
		Oracle: Oracle{
			Product: "NVDA", Version: "2026.1.1", Platform: "external reference",
			ReleaseTag: "release-2026.1.1", ReleaseCommit: "5d92106f17e461dac62aa48257bbbf4183e033d0",
			CaptureToolURL: captureToolURL, CaptureToolHash: captureToolHash, EvidenceLicense: "GPL-2.0-or-later",
		},
	}
	cell := 0
	for _, locale := range []string{"en-US", "de-DE"} {
		for _, layout := range []string{"desktop", "laptop"} {
			id := locale + "-" + layout
			caseExpected := expected
			caseExpected.CaseID, caseExpected.Locale, caseExpected.KeyboardLayout = id, locale, layout
			caseExpected.Steps = append([]TraceStep(nil), expected.Steps...)
			caseObserved := observed
			caseObserved.CaseID, caseObserved.Locale, caseObserved.KeyboardLayout = id, locale, layout
			caseObserved.Steps = append([]TraceStep(nil), observed.Steps...)
			caseObserved.Steps[0].Raw = json.RawMessage(`{"capture":"linux-container-chromium-at-spi","gesture":"h"}`)
			if mismatch && locale == "en-US" && layout == "desktop" {
				caseObserved.Steps = append([]TraceStep(nil), caseObserved.Steps...)
				caseObserved.Steps[0].Speech = []string{"Checkout heading"}
			}
			caseExpectedData := marshalJSON(t, caseExpected)
			caseObservedData := marshalJSON(t, caseObserved)
			expectedPath := id + "-expected.json"
			observedPath := id + "-observed.json"
			writeFile(t, filepath.Join(root, expectedPath), caseExpectedData)
			writeFile(t, filepath.Join(root, observedPath), caseObservedData)
			referencePath := "tests/system/robot/chromeTests.py"
			referenceTest := "test_aria_details_noVBufNoTextInterface"
			tags := []string{"speech", "braille", "browser-chrome", locale, layout}
			switch cell {
			case 0:
				tags = append(tags, "focus", "focus-mode", "forms")
			case 1:
				referenceTest = "test_quickNavTargetReporting"
				tags = append(tags, "browse-mode", "quick-navigation", "text-navigation")
			case 2:
				referenceTest = "test_tableInStyleDisplayTable"
				tags = append(tags, "tables")
			case 3:
				referencePath = "source/NVDAHelper/__init__.py"
				referenceTest = "nvdaControllerInternal_reportLiveRegion"
				tags = append(tags, "live-region", "dynamic-content")
			}
			caseExpected.Steps[0].Raw = json.RawMessage(`{"upstreamTest":"` + referenceTest + `","gesture":"h"}`)
			caseExpectedData = marshalJSON(t, caseExpected)
			writeFile(t, filepath.Join(root, expectedPath), caseExpectedData)
			item := Case{
				ID: id, Locale: locale, KeyboardLayout: layout,
				Fixture: "fixture.html", FixtureSHA256: digest(fixture),
				Expected: expectedPath, ExpectedSHA256: digest(caseExpectedData),
				Observed: observedPath, ObservedSHA256: digest(caseObservedData),
				Reference: Reference{
					URL: "https://github.com/nvaccess/nvda/blob/" + manifest.Oracle.ReleaseCommit + "/" + referencePath, Revision: manifest.Oracle.ReleaseCommit,
					Path: referencePath, SHA256: officialReferenceHashes[referencePath],
					Test: referenceTest, Assertion: "synthetic conformance validator fixture",
				},
				Tags: tags,
			}
			if cell == 1 {
				item.AdditionalReferences = []Reference{{
					URL: "https://github.com/nvaccess/nvda/blob/" + manifest.Oracle.ReleaseCommit + "/tests/system/robot/chromeTests.py", Revision: manifest.Oracle.ReleaseCommit,
					Path: "tests/system/robot/chromeTests.py", SHA256: officialReferenceHashes["tests/system/robot/chromeTests.py"],
					Test: "test_textParagraphNavigation", Assertion: "synthetic conformance validator fixture",
				}}
			}
			if locale == "de-DE" {
				item.Localization = &Localization{
					URL:      "https://github.com/nvaccess/nvda/blob/" + manifest.Oracle.ReleaseCommit + "/" + localizationPath,
					Revision: manifest.Oracle.ReleaseCommit, Path: localizationPath, SHA256: localizationHash, Locale: "de-DE",
				}
			}
			manifest.Cases = append(manifest.Cases, item)
			cell++
		}
	}
	manifestData := marshalJSON(t, manifest)
	path := filepath.Join(root, "manifest.json")
	writeFile(t, path, manifestData)
	return path
}

func TestReferenceHashOutsideAuditedRegistryFailsClosed(t *testing.T) {
	path := writeCorpus(t, false)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Cases[0].Reference.SHA256 = strings.Repeat("a", 64)
	writeFile(t, path, marshalJSON(t, manifest))

	report, err := Check(path)
	if err == nil || !slicesContainSubstring(report.Issues, "audited official source registry") {
		t.Fatalf("report=%#v err=%v", report, err)
	}
}

func TestSpeechOnlyEvidenceIsAllowedOnlyWithoutBrailleTag(t *testing.T) {
	item := Case{
		ID: "speech-only", Locale: "en-US", KeyboardLayout: "desktop", Tags: []string{"speech"},
		Reference: Reference{Test: "test_heading"},
	}
	trace := Trace{
		SchemaVersion: SchemaVersion, CaseID: item.ID, Source: "NVDA", Profile: "nvda-web-2026.1.1",
		Locale: item.Locale, KeyboardLayout: item.KeyboardLayout,
		Steps: []TraceStep{{
			Command: "nextHeading", Speech: []string{"Heading"}, Braille: []string{}, Mode: "browse",
			Raw: json.RawMessage(`{"upstreamTest":"test_heading","gesture":"h"}`),
		}},
	}
	if issue := validateTrace(trace, item, trace.Profile, "NVDA"); issue != "" {
		t.Fatal(issue)
	}
	item.Tags = append(item.Tags, "braille")
	if issue := validateTrace(trace, item, trace.Profile, "NVDA"); !strings.Contains(issue, "tagged braille") {
		t.Fatalf("issue=%q", issue)
	}
}

func TestSemanticCoverageTagsRequireCorrectPinnedEvidence(t *testing.T) {
	reference := Reference{Path: "tests/system/robot/chromeTests.py", Test: "test_aria_details_noVBufNoTextInterface"}
	if issue := validateCoverageEvidence("tables", []Reference{reference}); issue == "" {
		t.Fatal("ARIA details reference must not substantiate table coverage")
	}
	reference.Test = "test_tableInStyleDisplayTable"
	if issue := validateCoverageEvidence("tables", []Reference{reference}); issue != "" {
		t.Fatal(issue)
	}
}

func TestIncompleteStepChannelsFailClosed(t *testing.T) {
	for _, channel := range []string{"speech", "braille"} {
		for _, null := range []bool{false, true} {
			t.Run(channel+"-null-"+fmt.Sprint(null), func(t *testing.T) {
				path := writeCorpus(t, false)
				var manifest Manifest
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(data, &manifest); err != nil {
					t.Fatal(err)
				}
				item := &manifest.Cases[0]
				for _, trace := range []struct {
					path string
					hash *string
				}{{item.Expected, &item.ExpectedSHA256}, {item.Observed, &item.ObservedSHA256}} {
					tracePath := filepath.Join(filepath.Dir(path), trace.path)
					data, err := os.ReadFile(tracePath)
					if err != nil {
						t.Fatal(err)
					}
					var decoded map[string]any
					if err := json.Unmarshal(data, &decoded); err != nil {
						t.Fatal(err)
					}
					steps := decoded["steps"].([]any)
					step := map[string]any{}
					for key, value := range steps[0].(map[string]any) {
						step[key] = value
					}
					if null {
						step[channel] = nil
					} else {
						delete(step, channel)
					}
					decoded["steps"] = append(steps, step)
					data = marshalJSON(t, decoded)
					writeFile(t, tracePath, data)
					*trace.hash = digest(data)
				}
				writeFile(t, path, marshalJSON(t, manifest))
				report, err := Check(path)
				if err == nil || !slicesContainSubstring(report.Issues, channel+" must be an array") {
					t.Fatalf("incomplete step passed: report=%#v err=%v", report, err)
				}
			})
		}
	}
}

func TestDuplicateJSONFieldsFailClosed(t *testing.T) {
	for _, data := range []string{
		`{"status":"incomplete","status":"complete"}`,
		`{"oracle":{"product":"other","product":"NVDA"}}`,
		`{"cases":[{"id":"first","id":"second"}]}`,
	} {
		var decoded map[string]any
		if err := decodeStrict([]byte(data), &decoded); err == nil || !strings.Contains(err.Error(), "duplicate JSON field") {
			t.Fatalf("duplicate keys accepted: %s err=%v", data, err)
		}
	}
}

func TestNonCanonicalJSONFieldsFailClosed(t *testing.T) {
	for _, data := range []string{
		`{"status":"incomplete","Status":"complete"}`,
		`{"Status":"complete"}`,
		`{"oracle":{"product":"other","Product":"NVDA"}}`,
		`{"cases":[{"id":"first","ID":"second"}]}`,
	} {
		var decoded Manifest
		if err := decodeStrict([]byte(data), &decoded); err == nil {
			t.Fatalf("noncanonical typed fields accepted: %s", data)
		}
	}
}

func TestTaggedChannelsRequireNonBlankOutput(t *testing.T) {
	for _, channel := range []string{"speech", "braille"} {
		item := Case{ID: "empty-output", Locale: "en-US", KeyboardLayout: "desktop", Tags: []string{channel}, Reference: Reference{Test: "test_heading"}}
		step := TraceStep{Command: "nextHeading", Speech: []string{"Heading"}, Braille: []string{"Heading"}, Mode: "browse", Raw: json.RawMessage(`{"upstreamTest":"test_heading","gesture":"h"}`)}
		if channel == "speech" {
			step.Speech = []string{"", " \n\t"}
		} else {
			step.Braille = []string{"", " \n\t"}
		}
		trace := Trace{SchemaVersion: SchemaVersion, CaseID: item.ID, Source: "NVDA", Profile: "nvda-web-2026.1.1", Locale: item.Locale, KeyboardLayout: item.KeyboardLayout, Steps: []TraceStep{step}}
		if issue := validateTrace(trace, item, trace.Profile, "NVDA"); !strings.Contains(issue, "tagged "+channel+" has no") {
			t.Fatalf("blank %s claimed coverage: issue=%q", channel, issue)
		}
	}
}

func marshalJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(data, '\n')
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func digest(data []byte) string {
	value := sha256.Sum256(data)
	return hex.EncodeToString(value[:])
}

func slicesContainSubstring(values []string, substring string) bool {
	for _, value := range values {
		if strings.Contains(value, substring) {
			return true
		}
	}
	return false
}
