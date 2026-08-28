package conformance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
		Steps: []TraceStep{{Command: "nextHeading", Speech: []string{"Checkout heading level 1"}, Braille: []string{"Checkout"}, Mode: "browse"}},
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
	for _, locale := range []string{"en-US", "de-DE"} {
		for _, layout := range []string{"desktop", "laptop"} {
			id := locale + "-" + layout
			caseExpected := expected
			caseExpected.CaseID, caseExpected.Locale, caseExpected.KeyboardLayout = id, locale, layout
			caseObserved := observed
			caseObserved.CaseID, caseObserved.Locale, caseObserved.KeyboardLayout = id, locale, layout
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
			manifest.Cases = append(manifest.Cases, Case{
				ID: id, Locale: locale, KeyboardLayout: layout,
				Fixture: "fixture.html", FixtureSHA256: digest(fixture),
				Expected: expectedPath, ExpectedSHA256: digest(caseExpectedData),
				Observed: observedPath, ObservedSHA256: digest(caseObservedData),
				Reference: Reference{
					URL: "https://github.com/nvaccess/nvda/blob/" + manifest.Oracle.ReleaseCommit + "/tests/system/robot/chromeTests.py", Revision: manifest.Oracle.ReleaseCommit,
					Path: "tests/system/robot/chromeTests.py", SHA256: officialReferenceHashes["tests/system/robot/chromeTests.py"],
					Test: "test_checkout", Assertion: "heading speech and braille",
				},
				Tags: append([]string(nil), requiredTags...),
			})
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
