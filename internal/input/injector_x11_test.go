package input

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"testing"
	"time"
)

// Run only on a private Xvfb display: this exercises the installed XTest
// injector and changes the test server's Caps Lock indicator.
func TestLaptopCtrlHomePreservesCapsLock(t *testing.T) {
	if os.Getenv("HOOVDA_TEST_PRIVATE_XVFB") != "1" {
		t.Skip("requires a disposable Xvfb display")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	t.Setenv("LC_ALL", "C")
	capsState := regexp.MustCompile(`Caps Lock:\s+(on|off)\b`)
	caps := func() string {
		out, err := exec.CommandContext(ctx, "xset", "q").CombinedOutput()
		if err != nil {
			t.Fatalf("read XKB indicators: %v: %s", err, out)
		}
		match := capsState.FindSubmatch(out)
		if len(match) != 2 {
			t.Fatal("xset returned no Caps Lock indicator")
		}
		return string(match[1])
	}
	injector := XDoTool{KeyboardLayout: "laptop"}
	for _, state := range []string{"off", "on"} {
		if caps() != state {
			if err := exec.CommandContext(ctx, "xdotool", "key", "Caps_Lock").Run(); err != nil {
				t.Fatal(err)
			}
		}
		if got := caps(); got != state {
			t.Fatalf("test setup: Caps Lock = %s, want %s", got, state)
		}
		if err := injector.Press(ctx, "ctrl+home"); err != nil {
			t.Fatal(err)
		}
		if got := caps(); got != state {
			t.Fatalf("Ctrl+Home changed Caps Lock from %s to %s", state, got)
		}
	}
}
