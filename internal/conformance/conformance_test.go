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
	data := []byte(`{"schemaVersion":1,"profile":"nvda-web-2026.1.1","status":"incomplete","oracle":{"product":"NVDA","version":"2026.1.1","platform":"manual","captureToolHash":""},"cases":[]}`)
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
		Oracle: Oracle{Product: "NVDA", Version: "2026.1.1", Platform: "external reference", CaptureToolHash: strings.Repeat("a", 64)},
		Cases: []Case{{
			ID: "checkout", Locale: "en-US", KeyboardLayout: "desktop",
			Fixture: "fixture.html", FixtureSHA256: digest(fixture),
			Expected: "expected.json", ExpectedSHA256: digest(expectedData),
			Observed: "observed.json", ObservedSHA256: digest(observedData),
			Tags: append([]string(nil), requiredTags...),
		}},
	}
	manifestData := marshalJSON(t, manifest)
	path := filepath.Join(root, "manifest.json")
	writeFile(t, path, manifestData)
	return path
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
