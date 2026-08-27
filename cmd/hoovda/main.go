package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/openhoo/hoovda/internal/atspi"
	"github.com/openhoo/hoovda/internal/braille"
	"github.com/openhoo/hoovda/internal/buildinfo"
	"github.com/openhoo/hoovda/internal/config"
	"github.com/openhoo/hoovda/internal/conformance"
	"github.com/openhoo/hoovda/internal/engine"
	"github.com/openhoo/hoovda/internal/events"
	"github.com/openhoo/hoovda/internal/input"
	"github.com/openhoo/hoovda/internal/profile"
	"github.com/openhoo/hoovda/internal/recording"
	hooruntime "github.com/openhoo/hoovda/internal/runtime"
	"github.com/openhoo/hoovda/internal/server"
	"github.com/openhoo/hoovda/internal/synth"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "hoovda:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: hoovda <serve|doctor|conformance|version>")
	}
	switch args[0] {
	case "serve":
		return serve(args[1:])
	case "doctor":
		return doctor(args[1:])
	case "conformance":
		return checkConformance(args[1:])
	case "version":
		fmt.Printf("hoovda %s (%s, %s)\n", buildinfo.Version, buildinfo.Commit, buildinfo.BuildDate)
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func serve(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg, err := config.FromEnvironment()
	if err != nil {
		return err
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return fmt.Errorf("runtime requires linux/amd64, got %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	client, err := atspi.Connect(ctx, logger)
	if err != nil {
		return err
	}
	defer client.Close()
	accessibility := hooruntime.NewAccessibility(client)
	store := events.NewStore(cfg.EventsLimit)
	presenter, err := profile.NewPresenter(cfg.Locale)
	if err != nil {
		return err
	}
	translator := braille.Liblouis{Command: cfg.LiblouisCommand}
	synthesizer := synth.ESpeak{Command: cfg.ESpeakCommand}
	recorder, err := recording.NewManager(recording.Config{
		Root: cfg.ArtifactsRoot, Display: cfg.Display, Width: cfg.ViewportWidth,
		Height: cfg.ViewportHeight, FFmpeg: cfg.FFmpegCommand,
	})
	if err != nil {
		return err
	}
	screenreader := engine.New(accessibility, store, presenter, translator, synthesizer, recorder, logger, engine.Config{
		Locale: cfg.Locale, KeyboardLayout: cfg.KeyboardLayout, BrowserProcess: cfg.BrowserProcess,
		StartupTimeout: cfg.StartupTimeout, SynthRequest: synth.Request{Rate: 175, Pitch: 50, Volume: 100},
	})
	if err := screenreader.Start(ctx); err != nil {
		return fmt.Errorf("start screenreader: %w", err)
	}
	if _, err := client.RegisterDeviceListener(ctx, cfg.KeyboardLayout, screenreader.HandleGesture); err != nil {
		return err
	}
	api, err := server.New(ctx, server.Config{
		Token: cfg.ControlToken, Profile: cfg.Profile, Locale: cfg.Locale,
		KeyboardLayout: cfg.KeyboardLayout, ActionTimeout: cfg.ActionTimeout,
		QuietWindow: cfg.QuiescenceWindow,
	}, screenreader, store, &input.XDoTool{Command: cfg.XDoToolCommand}, recorder, logger)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", cfg.ControlAddress)
	if err != nil {
		return err
	}
	defer listener.Close()
	httpServer := &http.Server{
		Handler: api.Handler(), ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout: cfg.ActionTimeout + 2*time.Second, WriteTimeout: max(45*time.Second, 2*cfg.ActionTimeout+5*time.Second),
		IdleTimeout: 30 * time.Second,
	}
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	logger.Info("HooVDA control API ready", "address", cfg.ControlAddress, "profile", cfg.Profile, "locale", cfg.Locale, "keyboardLayout", cfg.KeyboardLayout)
	err = httpServer.Serve(listener)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	<-shutdownDone
	return nil
}

func doctor(args []string) error {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	if err := flags.Parse(args); err != nil {
		return err
	}
	commands := []string{"espeak-ng", "lou_translate", "ffmpeg", "xdotool", "dbus-daemon"}
	type check struct {
		Name   string `json:"name"`
		Path   string `json:"path,omitempty"`
		Status string `json:"status"`
	}
	result := struct {
		OS       string  `json:"os"`
		Arch     string  `json:"arch"`
		Display  string  `json:"display"`
		Commands []check `json:"commands"`
	}{OS: runtime.GOOS, Arch: runtime.GOARCH, Display: os.Getenv("DISPLAY")}
	failed := runtime.GOOS != "linux" || runtime.GOARCH != "amd64"
	for _, command := range commands {
		path, err := exec.LookPath(command)
		item := check{Name: command, Path: path, Status: "ok"}
		if err != nil {
			item.Status = "missing"
			failed = true
		}
		result.Commands = append(result.Commands, item)
	}
	if result.Display == "" {
		failed = true
	}
	encoded, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(encoded))
	if failed {
		return errors.New("doctor found missing runtime requirements")
	}
	return nil
}

func checkConformance(args []string) error {
	flags := flag.NewFlagSet("conformance", flag.ContinueOnError)
	manifest := flags.String("manifest", "oracle/corpus/manifest.json", "oracle manifest")
	if err := flags.Parse(args); err != nil {
		return err
	}
	report, checkErr := conformance.Check(*manifest)
	encoded, _ := json.MarshalIndent(report, "", "  ")
	fmt.Println(string(encoded))
	return checkErr
}
