// Command capture-fixtures records the real CLI's output for Quark's parser tests.
//
// It talks to the owner's REAL Proton Drive account, so it is built around
// the constitution's Principle I:
//   - every command goes through pdcli.SandboxRunner, which refuses anything
//     outside this run's /my-files/_quark-test-<run> folder;
//   - it asks for confirmation before doing anything;
//   - it always removes its sandbox folder at the end, also after Ctrl-C;
//   - outputs are sanitized before they touch the disk; listings outside the
//     sandbox keep only structure (no names, emails, ids, sizes or dates).
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"example.org/quark/internal/pdcli"
)

type step struct {
	Name    string   `json:"name"`
	Args    []string `json:"args"`
	Mode    Mode     `json:"-"`
	Expect  string   `json:"expect"` // "ok", "fail" or "any"
	Exit    int      `json:"exit"`
	Kind    string   `json:"kind,omitempty"`
	Comment string   `json:"comment,omitempty"`
}

func main() {
	cliPath := flag.String("cli", "", "path to proton-drive (default: auto-detect)")
	outRoot := flag.String("out", "internal/pdcli/testdata/cli", "fixtures root; a <version> subfolder is created")
	yes := flag.Bool("yes", false, "do not ask for confirmation")
	flag.Parse()

	if err := run(*cliPath, *outRoot, *yes); err != nil {
		fmt.Fprintln(os.Stderr, "capture-fixtures:", err)
		os.Exit(1)
	}
}

func run(cliPath, outRoot string, yes bool) error {
	bin := cliPath
	if bin == "" {
		var hints []string
		bin, hints = pdcli.FindBinary("")
		if bin == "" {
			return fmt.Errorf("proton-drive not found (hints: %v); pass -cli", hints)
		}
	}
	table := pdcli.NewProcessTable()
	defer table.Shutdown(10 * time.Second)
	execRunner := &pdcli.ExecRunner{Binary: bin, Table: table}

	local, err := os.MkdirTemp("", "quark-fixtures-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(local) }()

	sb := &pdcli.SandboxRunner{Inner: execRunner, Token: pdcli.NewSandboxToken(time.Now()), LocalRoot: local}
	tok, root := sb.Token, sb.Root()
	client := pdcli.NewClient(sb)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	v, err := client.Version(ctx)
	if err != nil {
		return fmt.Errorf("checking CLI version: %w", err)
	}
	if _, err := client.Roots(ctx); err != nil {
		if pdcli.IsKind(err, pdcli.KindNotLoggedIn) {
			return errors.New("not logged in: run `proton-drive auth login` first")
		}
		return fmt.Errorf("checking login: %w", err)
	}
	outDir := filepath.Join(outRoot, v.String())

	fmt.Printf(`Proton Drive CLI %s (%s)

This will use your REAL Proton Drive account:
  1. create the folder  %s
  2. upload 4 small test files into it, rename/copy/move them inside it,
     create a public link on one of them (no one is invited or emailed),
     trash and restore one, permanently delete one from the trash;
  3. read (without changing) /, /trash, /shared-with-me, /shared-by-me,
     /devices and your pending invitations; these are saved with every
     name, email, id, size and date replaced;
  4. move the test folder to the trash and permanently delete it.

Nothing outside that folder is modified. Fixtures are written to %s.
`, v.Raw, bin, root, outDir)
	if !yes && !confirm() {
		return errors.New("cancelled")
	}

	if err := writeLocalFiles(local, tok); err != nil {
		return err
	}
	if err := os.MkdirAll(outDir, 0o750); err != nil {
		return err
	}
	san := NewSanitizer(tok)
	rec := &recorder{runner: sb, san: san, dir: outDir, local: local}

	created := false
	defer func() {
		if created {
			cleanup(client, root, tok)
		}
	}()

	readOnly, create, steps := plan(tok, root, local, time.Now())
	for _, s := range readOnly {
		if err := rec.do(ctx, s); err != nil {
			return err
		}
	}

	// Create the sandbox. Cleanup is armed first: if we are interrupted while
	// the folder is being created, it may exist even though we saw no result.
	created = true
	if err := rec.do(ctx, create); err != nil {
		return err
	}
	if last := rec.steps[len(rec.steps)-1]; last.Kind != "" {
		return fmt.Errorf("could not create the sandbox folder (%s); see %s.stderr", last.Kind, filepath.Join(outDir, last.Name))
	}

	for _, s := range steps {
		if err := rec.do(ctx, s); err != nil {
			return err
		}
	}

	// UID addressing: Quark addresses nodes as /my-files/<uid>; verify that it
	// works for files and folders. Read-only commands only.
	if n, err := client.Info(ctx, root+"/"+tok+"-renamed.txt"); err == nil {
		_ = rec.do(ctx, step{Name: "info-by-uid", Args: inv("filesystem", "info", nil, "/my-files/"+n.UID), Expect: "ok"})
	}
	if n, err := client.Info(ctx, root+"/"+tok+"-sub"); err == nil {
		_ = rec.do(ctx, step{Name: "list-by-uid", Args: inv("filesystem", "list", nil, "/my-files/"+n.UID), Expect: "ok"})
	}

	return rec.writeManifest(v)
}

// plan returns the read-only steps, the sandbox creation step and the steps
// that run inside the sandbox. Every step must pass the sandbox guard
// (checked by TestPlanPassesSandboxGuard).
func plan(tok, root, local string, now time.Time) (readOnly []step, create step, steps []step) {
	f := func(name string) string { return filepath.Join(local, name) }
	exp := now.AddDate(0, 0, 7).UTC().Format("2006-01-02")

	// Read-only listings of real data: Foreign mode.
	readOnly = []step{
		{Name: "version", Args: []string{"--version"}, Expect: "ok", Mode: Sandbox},
		{Name: "list-root", Args: inv("filesystem", "list", nil, "/"), Expect: "ok", Mode: Foreign},
		{Name: "list-trash", Args: inv("filesystem", "list", nil, "/trash"), Expect: "ok", Mode: Foreign},
		{Name: "list-shared-with-me", Args: inv("filesystem", "list", nil, "/shared-with-me"), Expect: "ok", Mode: Foreign},
		{Name: "list-shared-by-me", Args: inv("filesystem", "list", nil, "/shared-by-me"), Expect: "ok", Mode: Foreign},
		{Name: "list-devices", Args: inv("filesystem", "list", nil, "/devices"), Expect: "ok", Mode: Foreign},
		{Name: "invitation-list", Args: inv("invitation", "list", nil), Expect: "ok", Mode: Foreign},
	}
	create = step{Name: "create-folder", Args: inv("filesystem", "create-folder", nil, "/my-files", tok), Expect: "ok"}

	a, sub, dir := root+"/"+tok+"-a.txt", root+"/"+tok+"-sub", root+"/"+tok+"-dir"
	steps = []step{
		{Name: "upload", Args: inv("filesystem", "upload", []string{"-f", "rename", "-d", "merge"}, f(tok+"-a.txt"), f(tok+"-b.bin"), f(tok+"-dir"), root), Expect: "ok"},
		{Name: "upload-identical-skipped", Args: inv("filesystem", "upload", []string{"-f", "rename", "-d", "merge"}, f(tok+"-a.txt"), root), Expect: "ok"},
		{Name: "upload-conflict-unforced", Args: inv("filesystem", "upload", nil, f("v2/"+tok+"-a.txt"), root), Expect: "fail", Comment: "conflict without strategy in JSON mode"},
		{Name: "upload-conflict-rename", Args: inv("filesystem", "upload", []string{"-f", "rename", "-d", "merge"}, f("v2/"+tok+"-a.txt"), root), Expect: "ok"},
		{Name: "upload-conflict-revision", Args: inv("filesystem", "upload", []string{"-f", "create-new-revision", "-d", "merge"}, f("v2/"+tok+"-a.txt"), root), Expect: "ok"},
		{Name: "list-sandbox", Args: inv("filesystem", "list", nil, root), Expect: "ok"},
		{Name: "list-folder-filter", Args: inv("filesystem", "list", []string{"-t", "folder"}, root), Expect: "ok"},
		{Name: "info-file", Args: inv("filesystem", "info", nil, a), Expect: "ok"},
		{Name: "info-folder", Args: inv("filesystem", "info", nil, dir), Expect: "ok"},
		{Name: "create-folder-sub", Args: inv("filesystem", "create-folder", nil, root, tok+"-sub"), Expect: "ok"},
		{Name: "create-folder-duplicate", Args: inv("filesystem", "create-folder", nil, root, tok+"-sub"), Expect: "fail"},
		{Name: "rename", Args: inv("filesystem", "rename", nil, a, tok+"-renamed.txt"), Expect: "ok"},
		{Name: "copy", Args: inv("filesystem", "copy", []string{"-n", tok + "-copy.txt"}, root+"/"+tok+"-renamed.txt", sub), Expect: "ok"},
		{Name: "move", Args: inv("filesystem", "move", nil, dir, sub), Expect: "ok"},
		{Name: "sharing-status-unshared", Args: inv("sharing", "status", nil, root+"/"+tok+"-b.bin"), Expect: "ok"},
		{Name: "sharing-set-url", Args: inv("sharing", "set-url", []string{"--role", "viewer", "--expiration", exp}, root+"/"+tok+"-renamed.txt"), Expect: "ok"},
		{Name: "sharing-status-shared", Args: inv("sharing", "status", nil, root+"/"+tok+"-renamed.txt"), Expect: "ok"},
		{Name: "list-shared-by-me-after", Args: inv("filesystem", "list", nil, "/shared-by-me"), Expect: "ok", Mode: Foreign},
		{Name: "download", Args: inv("filesystem", "download", []string{"-f", "rename", "-d", "merge"}, sub, f("out")), Expect: "ok"},
		{Name: "download-again-skip", Args: inv("filesystem", "download", []string{"-f", "skip", "-d", "merge"}, sub, f("out")), Expect: "ok"},
		{Name: "trash", Args: inv("filesystem", "trash", nil, sub+"/"+tok+"-copy.txt"), Expect: "ok"},
		{Name: "restore", Args: inv("filesystem", "restore", nil, "/trash/"+tok+"-copy.txt"), Expect: "ok"},
		{Name: "trash-again", Args: inv("filesystem", "trash", nil, sub+"/"+tok+"-copy.txt"), Expect: "ok"},
		{Name: "delete", Args: inv("filesystem", "delete", nil, "/trash/"+tok+"-copy.txt"), Expect: "ok"},
		{Name: "err-info-not-found", Args: inv("filesystem", "info", nil, root+"/"+tok+"-missing"), Expect: "fail"},
		{Name: "err-rename-not-found", Args: inv("filesystem", "rename", nil, root+"/"+tok+"-missing", tok+"-x"), Expect: "fail"},
		{Name: "err-delete-not-trashed", Args: inv("filesystem", "delete", nil, "/trash/"+tok+"-never-trashed"), Expect: "fail"},
		{Name: "err-list-trash-child", Args: inv("filesystem", "list", nil, "/trash/"+tok+"-x"), Expect: "fail"},
		{Name: "err-download-not-found", Args: inv("filesystem", "download", []string{"-f", "skip", "-d", "skip"}, root+"/"+tok+"-missing", f("out")), Expect: "fail"},
	}
	return readOnly, create, steps
}

// inv builds "<group> <command> --json [opts] -- [positionals]".
func inv(group, cmd string, opts []string, pos ...string) []string {
	a := []string{group, cmd, "--json"}
	a = append(a, opts...)
	a = append(a, "--")
	return append(a, pos...)
}

func confirm() bool {
	fmt.Print("Type yes to continue: ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(line) == "yes"
}

func writeLocalFiles(dir, tok string) error {
	files := map[string]string{
		tok + "-a.txt":                 "Quark fixture file A\n",
		tok + "-b.bin":                 strings.Repeat("\x00\x01\x02\x03", 4096),
		tok + "-dir/" + tok + "-c.txt": "Quark fixture file C\n",
		"v2/" + tok + "-a.txt":         "Quark fixture file A, second version\n",
	}
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			return err
		}
	}
	return os.MkdirAll(filepath.Join(dir, "out"), 0o700)
}

// cleanup trashes the sandbox and deletes it permanently. It uses a fresh
// context so that it also runs after Ctrl-C.
func cleanup(client *pdcli.Client, root, tok string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	fmt.Println("Cleaning up", root)
	if _, err := client.Trash(ctx, root); err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: could not trash %s: %v\nRemove it manually from Proton Drive.\n", root, err)
		return
	}
	if _, err := client.DeleteForever(ctx, "/trash/"+tok); err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: %s is in your trash but could not be deleted: %v\n", tok, err)
		return
	}
	if _, err := client.Info(ctx, root); !pdcli.IsKind(err, pdcli.KindNotFound) {
		fmt.Fprintf(os.Stderr, "WARNING: %s may still exist (info returned %v)\n", root, err)
		return
	}
	fmt.Println("Sandbox removed.")
}

type recorder struct {
	runner pdcli.Runner
	san    *Sanitizer
	dir    string
	local  string
	steps  []step
}

func (r *recorder) do(ctx context.Context, s step) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	res, err := r.runner.Run(ctx, pdcli.Request{Args: s.Args, Timeout: 5 * time.Minute, MaxStdout: pdcli.ListMaxStdout})
	if pdcli.IsKind(err, pdcli.KindRefused) {
		return fmt.Errorf("step %s refused by the sandbox guard: %w", s.Name, err)
	}
	if pdcli.IsKind(err, pdcli.KindCancelled) {
		return err
	}
	s.Exit = res.ExitCode
	if err != nil {
		s.Kind = pdcli.KindOf(err).String()
	}
	status := "ok"
	switch {
	case s.Expect == "ok" && err != nil:
		status = "UNEXPECTED FAILURE"
	case s.Expect == "fail" && err == nil:
		status = "UNEXPECTED SUCCESS"
	}
	fmt.Printf("  %-28s exit=%d %s %s\n", s.Name, s.Exit, s.Kind, status)

	base := filepath.Join(r.dir, s.Name)
	if werr := os.WriteFile(base+".stdout", r.san.Output(res.Stdout, s.Mode), 0o600); werr != nil {
		return werr
	}
	if werr := os.WriteFile(base+".stderr", []byte(r.san.Text(string(res.Stderr))), 0o600); werr != nil {
		return werr
	}
	s.Args = sanitizeArgs(r.san, r.local, s.Args)
	r.steps = append(r.steps, s)
	return nil
}

func sanitizeArgs(san *Sanitizer, local string, args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = san.Text(strings.ReplaceAll(a, local, "/tmp/quark-fixtures"))
	}
	return out
}

func (r *recorder) writeManifest(v pdcli.Version) error {
	m := struct {
		CLI      string `json:"cli"`
		SDK      string `json:"sdk"`
		Captured string `json:"captured"`
		Steps    []step `json:"steps"`
	}{CLI: v.Raw, SDK: v.SDK, Captured: time.Now().UTC().Format(time.DateOnly), Steps: r.steps}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(r.dir, "manifest.json"), append(b, '\n'), 0o600)
}
