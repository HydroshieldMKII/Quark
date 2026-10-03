// Command quark is an unofficial GNOME client for Proton Drive.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"example.org/quark/internal/app"
	"example.org/quark/internal/app/demo"
	"example.org/quark/internal/jobs"
	"example.org/quark/internal/pdcli"
	"example.org/quark/internal/settings"
	"example.org/quark/internal/ui"
	"example.org/quark/internal/xlog"
)

// version is set at build time with -ldflags "-X main.version=…".
var version = "0.1.0-dev"

func main() { os.Exit(run()) }

func run() int {
	verbose := flag.Bool("v", false, "also log to stderr, with debug detail")
	cliPath := flag.String("cli", "", "use this proton-drive binary for this session")
	demoMode := flag.Bool("demo", false, "show sample data; never runs the Proton Drive CLI")
	showVersion := flag.Bool("version", false, "print the version and exit")
	shot := flag.String("screenshot", "", "debug: render the window to this PNG file and quit")
	shotSection := flag.String("screenshot-section", "", "debug: sidebar entry to show before the screenshot")
	shotSize := flag.String("screenshot-size", "", "debug: window size for the screenshot, e.g. 1100x720")
	flag.Parse()
	if *showVersion {
		fmt.Println("Quark", version)
		return 0
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	logger, closeLog, err := xlog.New(xlog.Options{Stderr: *verbose, Level: level})
	if err != nil {
		logger.Warn("log file unavailable", "err", err)
	}
	defer func() { _ = closeLog() }()
	// gotk4 forwards GLib/GTK messages to slog.Default.
	slog.SetDefault(logger)
	logger.Info("starting", "version", version, "demo", *demoMode)

	var (
		st       settings.Settings
		save     func(settings.Settings) error
		statedir string
	)
	if *demoMode {
		st = settings.Defaults()
	} else {
		path, perr := settings.DefaultPath()
		if perr != nil {
			logger.Error("no config directory", "err", perr)
			st = settings.Defaults()
		} else {
			var lerr error
			st, lerr = settings.Load(path, time.Now())
			if lerr != nil {
				logger.Warn("settings", "err", lerr)
			}
			save = func(s settings.Settings) error { return settings.Save(path, s) }
		}
		if d, derr := xlog.DefaultDir(); derr == nil {
			statedir = d
		}
	}

	table := pdcli.NewProcessTable()
	var (
		runner     jobs.Runner
		findBinary = pdcli.FindBinary
		newBackend func(string, settings.Settings) app.Backend
	)
	if *demoMode {
		d := demo.New(time.Now())
		runner = d
		findBinary = func(string) (string, []string) { return "demo", nil }
		newBackend = func(string, settings.Settings) app.Backend { return d }
	} else {
		if *cliPath != "" {
			abs, aerr := filepath.Abs(*cliPath)
			if aerr == nil {
				findBinary = func(string) (string, []string) { return abs, nil }
			}
		}
		newBackend = func(bin string, s settings.Settings) app.Backend {
			c := pdcli.NewClient(&pdcli.ExecRunner{
				Binary: bin, Table: table,
				ExtraEnv: map[string]string{"PROTON_DRIVE_LOG_LEVEL": s.CLILogLevel},
			})
			runner = c
			return c
		}
	}

	loop := ui.MainLoop{}
	a := app.New(app.Config{
		Dispatcher: loop, Logger: logger, Settings: st, SaveSettings: save,
		FindBinary: findBinary, NewBackend: newBackend,
	})
	queuePath := ""
	if statedir != "" {
		queuePath = filepath.Join(statedir, "transfers.json")
	}
	q := jobs.NewQueue(jobs.Config{
		Dispatcher: loop, Logger: logger, StatePath: queuePath, Concurrency: st.MaxTransfers,
		Runner: func() jobs.Runner { return runner },
	})

	w, h := parseSize(*shotSize)
	code := ui.Run(ui.Options{
		App: a, Queue: q, Logger: logger, Version: version, Demo: *demoMode,
		Screenshot: *shot, ScreenshotSection: *shotSection, ScreenshotWidth: w, ScreenshotHeight: h,
		OnShutdown: func() {
			okQ := q.Shutdown(10 * time.Second)
			okA := a.Shutdown(10 * time.Second)
			left := table.Shutdown(5 * time.Second)
			logger.Info("stopped", "transfers", okQ, "tasks", okA, "processesLeft", left)
		},
	}, []string{os.Args[0]})
	return code
}

func parseSize(s string) (int, int) {
	ws, hs, ok := strings.Cut(s, "x")
	if !ok {
		return 0, 0
	}
	w, err1 := strconv.Atoi(ws)
	h, err2 := strconv.Atoi(hs)
	if err1 != nil || err2 != nil {
		return 0, 0
	}
	return w, h
}
