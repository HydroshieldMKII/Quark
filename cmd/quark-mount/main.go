// Command quark-mount shows Proton Drive as a folder, so that Files and any
// program can use it. It runs on the host (FUSE cannot be used from inside
// the Flatpak sandbox) and uses the Proton Drive CLI like the Quark app.
//
//	quark-mount ~/ProtonDrive
//
// Files are downloaded when opened and uploaded as a new version when
// closed; deleting moves to the trash. Unmount with Ctrl-C or
// `fusermount3 -u ~/ProtonDrive`.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"example.org/quark/internal/fusefs"
	"example.org/quark/internal/pdcli"
	"example.org/quark/internal/xlog"
)

var version = "0.1.0-dev"

func main() { os.Exit(run()) }

func run() int {
	cliPath := flag.String("cli", "", "path to proton-drive (default: auto-detect)")
	cacheDir := flag.String("cache", "", "content cache directory (default: $XDG_CACHE_HOME/quark/mount)")
	root := flag.String("root", "/my-files", "remote folder to show at the mount point")
	readOnly := flag.Bool("read-only", false, "refuse every change")
	ttl := flag.Duration("ttl", 10*time.Second, "how long folder listings are trusted")
	limit := flag.Int64("cache-limit", 1<<30, "content cache limit in bytes")
	verbose := flag.Bool("v", false, "log to stderr")
	debug := flag.Bool("debug", false, "log every FUSE operation (very verbose)")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: quark-mount [options] MOUNTPOINT\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *showVersion {
		fmt.Println("quark-mount", version)
		return 0
	}
	if flag.NArg() != 1 {
		flag.Usage()
		return 2
	}
	mountpoint, err := filepath.Abs(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	logger, closeLog, lerr := xlog.New(xlog.Options{Stderr: *verbose || *debug, Level: slog.LevelInfo})
	if lerr != nil {
		logger.Warn("log file unavailable", "err", lerr)
	}
	defer func() { _ = closeLog() }()

	if *cacheDir == "" {
		c, err := os.UserCacheDir()
		if err != nil {
			fmt.Fprintln(os.Stderr, "no cache directory:", err)
			return 1
		}
		*cacheDir = filepath.Join(c, "quark", "mount")
	}
	if err := os.MkdirAll(*cacheDir, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if st, err := os.Stat(mountpoint); err != nil || !st.IsDir() {
		fmt.Fprintf(os.Stderr, "%s is not a directory (create it first)\n", mountpoint)
		return 1
	}
	if entries, _ := os.ReadDir(mountpoint); len(entries) > 0 {
		fmt.Fprintf(os.Stderr, "%s is not empty\n", mountpoint)
		return 1
	}

	bin, hints := pdcli.FindBinary(*cliPath)
	if bin == "" {
		fmt.Fprintf(os.Stderr, "proton-drive not found (hints: %v); pass -cli\n", hints)
		return 1
	}
	table := pdcli.NewProcessTable()
	client := pdcli.NewClient(&pdcli.ExecRunner{Binary: bin, Table: table})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if _, err := client.Roots(ctx); err != nil {
		if pdcli.IsKind(err, pdcli.KindNotLoggedIn) {
			fmt.Fprintln(os.Stderr, "not signed in: run `proton-drive auth login` first")
		} else {
			fmt.Fprintln(os.Stderr, "cannot reach Proton Drive:", err)
		}
		return 1
	}

	m, err := fusefs.Mount(mountpoint, fusefs.Options{
		Backend: client, CacheDir: *cacheDir, Root: *root, ReadOnly: *readOnly,
		ListTTL: *ttl, CacheLimit: *limit, Logger: logger, Debug: *debug,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "mount failed:", err)
		return 1
	}
	fmt.Printf("Proton Drive mounted at %s (Ctrl-C to unmount)\n", mountpoint)
	go func() {
		<-ctx.Done()
		fmt.Println("unmounting…")
		for i := 0; i < 10; i++ {
			if err := m.Unmount(); err == nil {
				return
			}
			time.Sleep(time.Second) // busy: a program still has files open
		}
		fmt.Fprintln(os.Stderr, "could not unmount: close programs using the folder, then run: fusermount3 -u", mountpoint)
	}()
	m.Wait()
	left := table.Shutdown(10 * time.Second)
	logger.Info("unmounted", "processesLeft", left)
	return 0
}
