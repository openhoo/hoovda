package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	ProfileNVDAWeb202611 = "nvda-web-2026.1.1"
	LocaleEnglishUS      = "en-US"
	LocaleGermanDE       = "de-DE"
)

type Config struct {
	ControlAddress   string
	ControlToken     string
	Profile          string
	Locale           string
	KeyboardLayout   string
	Display          string
	ViewportWidth    int
	ViewportHeight   int
	EventsLimit      int
	ArtifactsRoot    string
	ESpeakCommand    string
	LiblouisCommand  string
	FFmpegCommand    string
	XDoToolCommand   string
	BrowserProcess   string
	StartupTimeout   time.Duration
	ActionTimeout    time.Duration
	QuiescenceWindow time.Duration
}

func FromEnvironment() (Config, error) {
	viewportWidth, err := envInt("HOOVDA_VIEWPORT_WIDTH", 1280)
	if err != nil {
		return Config{}, err
	}
	viewportHeight, err := envInt("HOOVDA_VIEWPORT_HEIGHT", 720)
	if err != nil {
		return Config{}, err
	}
	eventsLimit, err := envInt("HOOVDA_EVENTS_LIMIT", 100_000)
	if err != nil {
		return Config{}, err
	}
	startupTimeout, err := envDuration("HOOVDA_STARTUP_TIMEOUT", 60*time.Second)
	if err != nil {
		return Config{}, err
	}
	actionTimeout, err := envDuration("HOOVDA_ACTION_TIMEOUT", 15*time.Second)
	if err != nil {
		return Config{}, err
	}
	quiescenceWindow, err := envDuration("HOOVDA_QUIET_WINDOW", 300*time.Millisecond)
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		ControlAddress:  env("HOOVDA_CONTROL_ADDRESS", "127.0.0.1:3000"),
		ControlToken:    os.Getenv("HOOVDA_CONTROL_TOKEN"),
		Profile:         env("HOOVDA_PROFILE", ProfileNVDAWeb202611),
		Locale:          env("HOOVDA_LOCALE", LocaleEnglishUS),
		KeyboardLayout:  env("HOOVDA_KEYBOARD_LAYOUT", "desktop"),
		Display:         env("DISPLAY", ":99"),
		ViewportWidth:   viewportWidth,
		ViewportHeight:  viewportHeight,
		EventsLimit:     eventsLimit,
		ArtifactsRoot:   env("HOOVDA_ARTIFACTS_ROOT", "/tmp/hoovda/artifacts"),
		ESpeakCommand:   env("HOOVDA_ESPEAK_COMMAND", "espeak-ng"),
		LiblouisCommand: env("HOOVDA_LIBLOUIS_COMMAND", "lou_translate"),
		FFmpegCommand:   env("HOOVDA_FFMPEG_COMMAND", "ffmpeg"),
		XDoToolCommand:  env("HOOVDA_XDOTOOL_COMMAND", "xdotool"),
		BrowserProcess:  env("HOOVDA_BROWSER_PROCESS", "chromium"),
		StartupTimeout:  startupTimeout,
		// Graph-backed commands may spend up to five seconds rebuilding the
		// browser accessibility tree. Keep the HTTP action budget comfortably
		// above that internal deadline so a timed-out refresh can still publish
		// its commandSettled event and the caller receives the real cause.
		ActionTimeout:    actionTimeout,
		QuiescenceWindow: quiescenceWindow,
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	var errs []error
	if c.ControlToken == "" {
		errs = append(errs, errors.New("HOOVDA_CONTROL_TOKEN is required"))
	}
	if host, port, err := net.SplitHostPort(c.ControlAddress); err != nil {
		errs = append(errs, fmt.Errorf("invalid control address: %w", err))
	} else if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		errs = append(errs, errors.New("control address must bind to loopback"))
	} else if parsed, err := strconv.Atoi(port); err != nil || parsed < 1 || parsed > 65535 {
		errs = append(errs, errors.New("control port must be between 1 and 65535"))
	}
	if c.Profile != ProfileNVDAWeb202611 {
		errs = append(errs, fmt.Errorf("unsupported profile %q", c.Profile))
	}
	if c.Locale != LocaleEnglishUS && c.Locale != LocaleGermanDE {
		errs = append(errs, fmt.Errorf("unsupported locale %q", c.Locale))
	}
	if c.KeyboardLayout != "desktop" && c.KeyboardLayout != "laptop" {
		errs = append(errs, fmt.Errorf("unsupported keyboard layout %q", c.KeyboardLayout))
	}
	if c.ViewportWidth < 320 || c.ViewportWidth > 8192 || c.ViewportHeight < 240 || c.ViewportHeight > 8192 {
		errs = append(errs, errors.New("viewport is outside supported bounds"))
	}
	if c.EventsLimit < 100 || c.EventsLimit > 1_000_000 {
		errs = append(errs, errors.New("events limit must be between 100 and 1000000"))
	}
	if strings.TrimSpace(c.ArtifactsRoot) == "" {
		errs = append(errs, errors.New("artifacts root is required"))
	}
	if c.StartupTimeout < time.Second || c.StartupTimeout > 10*time.Minute {
		errs = append(errs, errors.New("startup timeout must be between 1s and 10m"))
	}
	if c.ActionTimeout <= 5*time.Second || c.ActionTimeout > time.Minute {
		errs = append(errs, errors.New("action timeout must be greater than 5s and at most 1m"))
	}
	if c.QuiescenceWindow < 10*time.Millisecond || c.QuiescenceWindow > 5*time.Second {
		errs = append(errs, errors.New("quiet window must be between 10ms and 5s"))
	}
	return errors.Join(errs...)
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func envInt(name string, fallback int) (int, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", name, err)
	}
	return parsed, nil
}

func envDuration(name string, fallback time.Duration) (time.Duration, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration: %w", name, err)
	}
	return parsed, nil
}
