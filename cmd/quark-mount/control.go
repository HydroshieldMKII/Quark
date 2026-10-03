package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"syscall"

	"example.org/quark/internal/fusefs"
)

// control runs the sub-commands that talk to a running mount through its
// extended attributes. It reports whether args named one.
func control(args []string, stdout, stderr io.Writer) (int, bool) {
	cmd := args[0]
	switch cmd {
	case "status", "sync", "pin", "unpin", "evict", "state":
	default:
		return 0, false
	}
	if len(args) < 2 {
		_, _ = fmt.Fprintf(stderr, "usage: quark-mount %s PATH\n", cmd)
		return 2, true
	}
	code := 0
	for _, path := range args[1:] {
		if err := controlOne(cmd, path, stdout); err != nil {
			_, _ = fmt.Fprintf(stderr, "%s: %v\n", path, err)
			code = 1
		}
	}
	return code, true
}

func controlOne(cmd, path string, stdout io.Writer) error {
	// A plain directory would happily store user.* attributes, so make sure
	// this is a mount first.
	if _, err := getxattr(path, "user.quark.state"); err != nil {
		if cmd == "status" || cmd == "sync" {
			return describe(err, "not the root of a Quark mount")
		}
		return describe(err, "not inside a Quark mount")
	}
	switch cmd {
	case "status":
		raw, err := getxattr(path, "user.quark.status")
		if err != nil {
			return describe(err, "not the root of a Quark mount")
		}
		var st fusefs.Status
		if err := json.Unmarshal(raw, &st); err != nil {
			return err
		}
		printStatus(stdout, st)
		return nil
	case "state":
		raw, err := getxattr(path, "user.quark.state")
		if err != nil {
			return describe(err, "not inside a Quark mount")
		}
		_, _ = fmt.Fprintf(stdout, "%s\t%s\n", raw, path)
		if e, err := getxattr(path, "user.quark.error"); err == nil {
			_, _ = fmt.Fprintf(stdout, "\tlast error: %s\n", e)
		}
		return nil
	case "sync":
		return describe(syscall.Setxattr(path, "user.quark.sync", []byte("1"), 0), "not the root of a Quark mount")
	case "pin":
		return describe(syscall.Setxattr(path, "user.quark.pin", []byte("1"), 0), "not inside a Quark mount")
	case "unpin":
		return describe(syscall.Setxattr(path, "user.quark.pin", []byte("0"), 0), "not inside a Quark mount")
	case "evict":
		err := syscall.Setxattr(path, "user.quark.evict", []byte("1"), 0)
		if errors.Is(err, syscall.EBUSY) {
			return errors.New("in use or not uploaded yet")
		}
		return describe(err, "not inside a Quark mount")
	}
	return nil
}

func describe(err error, notMount string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, syscall.ENODATA) || errors.Is(err, syscall.ENOTSUP) {
		return errors.New(notMount)
	}
	return err
}

func getxattr(path, name string) ([]byte, error) {
	buf := make([]byte, 64<<10)
	n, err := syscall.Getxattr(path, name, buf)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

func printStatus(w io.Writer, st fusefs.Status) {
	pf := func(format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }
	mode := "read-write"
	if st.ReadOnly {
		mode = "read-only"
	}
	pf("root:         %s (%s)\n", st.Root, mode)
	pf("pending:      %d change(s), %s", st.Pending, humanBytes(st.PendingBytes))
	if st.Errors > 0 {
		pf(", %d waiting to retry", st.Errors)
	}
	pf("\n")
	pf("pinned:       %d item(s)\n", st.Pinned)
	pf("downloading:  %d\n", st.Downloading)
	pf("cache:        %s of %s\n", humanBytes(st.CachedBytes), humanBytes(st.CacheLimit))
	switch {
	case st.Syncing:
		pf("sync:         running\n")
	case st.LastSync == "":
		pf("sync:         not yet\n")
	case st.LastSyncErr != "":
		pf("sync:         %s, last error: %s\n", st.LastSync, st.LastSyncErr)
	default:
		pf("sync:         %s\n", st.LastSync)
	}
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
