// Command quark-mount shows Proton Drive as a folder, so that Files and any
// program can use it. It runs on the host (FUSE cannot be used from inside
// the Flatpak sandbox) and uses the Proton Drive CLI like the Quark app.
//
//	quark-mount ~/ProtonDrive                 mount (Ctrl-C or fusermount3 -u to stop)
//	quark-mount status ~/ProtonDrive          what is pending, pinned, cached
//	quark-mount pin ~/ProtonDrive/Photos      keep on this device (files or folders)
//	quark-mount unpin ~/ProtonDrive/Photos    stop keeping it
//	quark-mount evict ~/ProtonDrive/big.iso   drop the local copy
//	quark-mount sync ~/ProtonDrive            retry pending uploads and check pins now
//	quark-mount state ~/ProtonDrive/a.txt     cloud | cached | pinned | pending | ...
//
// Files download when opened and are kept in a bounded cache; a saved file
// is journaled locally and uploaded as a new version, at once when online
// and otherwise retried in the background. Deleting moves to the trash.
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

	"example.org/quark/internal/fsutil"
	"example.org/quark/internal/fusefs"
	"example.org/quark/internal/pdcli"
	"example.org/quark/internal/xlog"
)

var version = "0.1.0-dev"

// exitNotSignedIn is EX_CONFIG: the unit file lists it in RestartPreventExitStatus so that a
// missing sign-in is one failed state instead of a restart loop.
const exitNotSignedIn = 78

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func usage(w *os.File) {
	_, _ = fmt.Fprintf(w, `usage: quark-mount [options] MOUNTPOINT
       quark-mount status|sync MOUNTPOINT
       quark-mount pin|unpin|evict|state PATH...

Options:
`)
}

func run(args []string, stdout, stderr *os.File) int {
	if len(args) > 0 {
		if code, handled := control(args, stdout, stderr); handled {
			return code
		}
	}
	fs := flag.NewFlagSet("quark-mount", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cliPath := fs.String("cli", "", "path to proton-drive (default: auto-detect)")
	cacheDir := fs.String("cache", "", "content cache directory (default: $XDG_CACHE_HOME/quark/mount)")
	root := fs.String("root", "/my-files", "remote folder to show at the mount point")
	readOnly := fs.Bool("read-only", false, "refuse every change")
	ttl := fs.Duration("ttl", 10*time.Second, "how long folder listings are trusted")
	syncEvery := fs.Duration("sync-interval", time.Minute, "how often pinned content is checked")
	limit := fs.Int64("cache-limit", 1<<30, "content cache limit in bytes (pinned files do not count)")
	verbose := fs.Bool("v", false, "log to stderr")
	debug := fs.Bool("debug", false, "log every FUSE operation (very verbose)")
	showVersion := fs.Bool("version", false, "print the version and exit")
	fs.Usage = func() { usage(stderr); fs.PrintDefaults() }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		_, _ = fmt.Fprintln(stdout, "quark-mount", version)
		return 0
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	mountpoint, err := filepath.Abs(fs.Arg(0))
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
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
			_, _ = fmt.Fprintln(stderr, "no cache directory:", err)
			return 1
		}
		*cacheDir = filepath.Join(c, "quark", "mount")
	}
	if err := os.MkdirAll(*cacheDir, fsutil.DirPrivate); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	if st, err := os.Stat(mountpoint); err != nil || !st.IsDir() {
		_, _ = fmt.Fprintf(stderr, "%s is not a directory (create it first)\n", mountpoint)
		return 1
	}
	if entries, _ := os.ReadDir(mountpoint); len(entries) > 0 {
		_, _ = fmt.Fprintf(stderr, "%s is not empty\n", mountpoint)
		return 1
	}

	bin, hints := pdcli.FindBinary(*cliPath)
	if bin == "" {
		_, _ = fmt.Fprintf(stderr, "proton-drive not found (hints: %v); pass -cli\n", hints)
		return 1
	}
	table := pdcli.NewProcessTable()
	client := pdcli.NewClient(&pdcli.ExecRunner{Binary: bin, Table: table, Logger: logger})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Being offline at start is fine: saved listings and cached content
	// still work and uploads resume. Only a missing sign-in is fatal.
	if _, err := client.Roots(ctx); err != nil {
		if pdcli.IsKind(err, pdcli.KindNotLoggedIn) {
			_, _ = fmt.Fprintln(stderr, "not signed in: run `proton-drive auth login` first")
			return exitNotSignedIn
		}
		logger.Warn("Proton Drive not reachable at start; mounting anyway", "err", err)
		_, _ = fmt.Fprintln(stderr, "Proton Drive not reachable; mounting offline:", err)
	}

	m, err := fusefs.Mount(mountpoint, fusefs.Options{
		Backend: client, CacheDir: *cacheDir, Root: *root, ReadOnly: *readOnly,
		ListTTL: *ttl, CacheLimit: *limit, SyncInterval: *syncEvery, Logger: logger, Debug: *debug,
	})
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "mount failed:", err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "Proton Drive mounted at %s (Ctrl-C to unmount)\n", mountpoint)
	go func() {
		<-ctx.Done()
		_, _ = fmt.Fprintln(stdout, "unmounting…")
		for i := 0; i < 10; i++ {
			if err := m.Unmount(); err == nil {
				return
			}
			time.Sleep(time.Second) // busy: a program still has files open
		}
		_, _ = fmt.Fprintln(stderr, "could not unmount: close programs using the folder, then run: fusermount3 -u", mountpoint)
	}()
	m.Wait()
	left := table.Shutdown(10 * time.Second)
	logger.Info("unmounted", "processesLeft", left)
	return 0
}
