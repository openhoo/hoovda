package config

import (
	"testing"
	"time"
)

func TestValidateRejectsRemoteControlBind(t *testing.T) {
	cfg := Config{
		ControlAddress:   "0.0.0.0:3000",
		ControlToken:     "token",
		Profile:          ProfileNVDAWeb202611,
		Locale:           LocaleEnglishUS,
		KeyboardLayout:   "desktop",
		ViewportWidth:    1280,
		ViewportHeight:   720,
		EventsLimit:      1000,
		ArtifactsRoot:    "/tmp/test",
		StartupTimeout:   time.Second,
		ActionTimeout:    6 * time.Second,
		QuiescenceWindow: 10 * time.Millisecond,
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected remote bind to be rejected")
	}
}

func TestFromEnvironmentRejectsMalformedDuration(t *testing.T) {
	t.Setenv("HOOVDA_CONTROL_TOKEN", "token")
	t.Setenv("HOOVDA_ACTION_TIMEOUT", "eventually")
	if _, err := FromEnvironment(); err == nil {
		t.Fatal("expected malformed duration to fail closed")
	}
}

func TestFromEnvironmentRejectsMalformedInteger(t *testing.T) {
	t.Setenv("HOOVDA_CONTROL_TOKEN", "token")
	t.Setenv("HOOVDA_EVENTS_LIMIT", "many")
	if _, err := FromEnvironment(); err == nil {
		t.Fatal("expected malformed integer to fail closed")
	}
}

func TestValidateAcceptsLockedProfile(t *testing.T) {
	cfg := Config{
		ControlAddress:   "127.0.0.1:3000",
		ControlToken:     "token",
		Profile:          ProfileNVDAWeb202611,
		Locale:           LocaleGermanDE,
		KeyboardLayout:   "laptop",
		ViewportWidth:    1280,
		ViewportHeight:   720,
		EventsLimit:      1000,
		ArtifactsRoot:    "/tmp/test",
		StartupTimeout:   time.Second,
		ActionTimeout:    6 * time.Second,
		QuiescenceWindow: 10 * time.Millisecond,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}
