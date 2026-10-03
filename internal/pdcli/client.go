package pdcli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"example.org/quark/internal/drive"
)

// Client is the typed API over the CLI. Every method validates its inputs
// before spawning anything and returns errors of type *Error.
type Client struct {
	runner Runner
	// MetaTimeout bounds metadata and sharing commands (default 2 minutes).
	MetaTimeout time.Duration
	// LoginTimeout bounds the browser sign-in (default 10 minutes).
	LoginTimeout time.Duration
}

// NewClient returns a Client using r.
func NewClient(r Runner) *Client {
	return &Client{runner: r, MetaTimeout: 2 * time.Minute, LoginTimeout: 10 * time.Minute}
}

func usageErr(command, format string, args ...any) error {
	return &Error{Kind: KindUsage, Command: command, Message: fmt.Sprintf(format, args...)}
}

// argv builds "<group> <command> --json [opts] -- [positionals]". The "--"
// keeps names that start with "-" from being read as options.
func argv(group, command string, opts []string, pos ...string) []string {
	out := make([]string, 0, 4+len(opts)+len(pos))
	out = append(out, group, command, "--json")
	out = append(out, opts...)
	out = append(out, "--")
	return append(out, pos...)
}

func (c *Client) meta(ctx context.Context, maxOut int, args []string) ([]byte, error) {
	res, err := c.runner.Run(ctx, Request{Args: args, Timeout: c.MetaTimeout, MaxStdout: maxOut})
	if err != nil {
		return res.Stdout, err
	}
	return res.Stdout, nil
}

func checkRemote(command string, paths ...string) error {
	if len(paths) == 0 {
		return usageErr(command, "no remote path given")
	}
	for _, p := range paths {
		if !strings.HasPrefix(p, "/") || strings.ContainsRune(p, 0) {
			return usageErr(command, "invalid remote path")
		}
	}
	return nil
}

func checkLocal(command string, paths ...string) error {
	if len(paths) == 0 {
		return usageErr(command, "no local path given")
	}
	for _, p := range paths {
		if !filepath.IsAbs(p) || strings.ContainsRune(p, 0) {
			return usageErr(command, "local paths must be absolute")
		}
	}
	return nil
}

func checkName(command, name string) error {
	if strings.TrimSpace(name) == "" || strings.ContainsAny(name, "\x00/") || name == "." || name == ".." {
		return usageErr(command, "invalid name")
	}
	return nil
}

// ----- metadata -----

// Roots lists the top-level sections. It doubles as the cheapest login check.
func (c *Client) Roots(ctx context.Context) ([]string, error) {
	out, err := c.meta(ctx, 0, argv("filesystem", "list", nil, "/"))
	if err != nil {
		return nil, err
	}
	return ParseRoots(out)
}

// List lists a folder or a section root.
func (c *Client) List(ctx context.Context, path string) ([]drive.Node, error) {
	const cmd = "filesystem list"
	if err := checkRemote(cmd, path); err != nil {
		return nil, err
	}
	if path == "/" || path == drive.SectionDevices.Root() {
		return nil, usageErr(cmd, "use Roots or Devices for %s", path)
	}
	out, err := c.meta(ctx, ListMaxStdout, argv("filesystem", "list", nil, path))
	if err != nil {
		return nil, err
	}
	return ParseNodes(out)
}

// Devices lists synced computers.
func (c *Client) Devices(ctx context.Context) ([]drive.Device, error) {
	out, err := c.meta(ctx, 0, argv("filesystem", "list", nil, drive.SectionDevices.Root()))
	if err != nil {
		return nil, err
	}
	return ParseDevices(out)
}

// Info returns full metadata for one node.
func (c *Client) Info(ctx context.Context, path string) (drive.Node, error) {
	const cmd = "filesystem info"
	if err := checkRemote(cmd, path); err != nil {
		return drive.Node{}, err
	}
	out, err := c.meta(ctx, 0, argv("filesystem", "info", nil, path))
	if err != nil {
		return drive.Node{}, err
	}
	return ParseNode(cmd, out)
}

// ----- mutations -----

// CreateFolder creates name inside parent and returns the new folder.
func (c *Client) CreateFolder(ctx context.Context, parent, name string) (drive.Node, error) {
	const cmd = "filesystem create-folder"
	if err := checkRemote(cmd, parent); err != nil {
		return drive.Node{}, err
	}
	if err := checkName(cmd, name); err != nil {
		return drive.Node{}, err
	}
	out, err := c.meta(ctx, 0, argv("filesystem", "create-folder", nil, parent, name))
	if err != nil {
		return drive.Node{}, err
	}
	return ParseNode(cmd, out)
}

// Rename renames a node in place.
func (c *Client) Rename(ctx context.Context, path, newName string) (drive.Node, error) {
	const cmd = "filesystem rename"
	if err := checkRemote(cmd, path); err != nil {
		return drive.Node{}, err
	}
	if err := checkName(cmd, newName); err != nil {
		return drive.Node{}, err
	}
	out, err := c.meta(ctx, 0, argv("filesystem", "rename", nil, path, newName))
	if err != nil {
		return drive.Node{}, err
	}
	return ParseNode(cmd, out)
}

func (c *Client) batch(ctx context.Context, command string, opts []string, pos []string) ([]drive.ItemResult, error) {
	group, name, _ := strings.Cut(command, " ")
	out, err := c.meta(ctx, 0, argv(group, name, opts, pos...))
	if err != nil {
		// Batch commands stream results; keep what completed if parseable.
		if res, perr := ParseBatch(command, out); perr == nil && len(res) > 0 {
			return res, err
		}
		return nil, err
	}
	res, err := ParseBatch(command, out)
	if err != nil {
		return nil, err
	}
	return res, batchErr(command, res)
}

// batchErr returns a KindPartial error if any item failed. The CLI does not
// report per-item reasons in JSON mode.
func batchErr(command string, res []drive.ItemResult) error {
	failed := 0
	for _, r := range res {
		if !r.OK {
			failed++
		}
	}
	if failed == 0 {
		return nil
	}
	return &Error{Kind: KindPartial, Command: command, Message: fmt.Sprintf("%d of %d item(s) failed", failed, len(res))}
}

// Copy copies sources into target. name renames the copy (single source only).
func (c *Client) Copy(ctx context.Context, sources []string, target, name string) ([]drive.ItemResult, error) {
	const cmd = "filesystem copy"
	if err := checkRemote(cmd, append(append([]string(nil), sources...), target)...); err != nil {
		return nil, err
	}
	var opts []string
	if name != "" {
		if len(sources) != 1 {
			return nil, usageErr(cmd, "a name can only be given when copying one item")
		}
		if err := checkName(cmd, name); err != nil {
			return nil, err
		}
		opts = []string{"--name", name}
	}
	return c.batch(ctx, cmd, opts, append(append([]string(nil), sources...), target))
}

// Move moves sources into target (same owner only).
func (c *Client) Move(ctx context.Context, sources []string, target string) ([]drive.ItemResult, error) {
	const cmd = "filesystem move"
	if err := checkRemote(cmd, append(append([]string(nil), sources...), target)...); err != nil {
		return nil, err
	}
	if len(sources) == 0 {
		return nil, usageErr(cmd, "nothing to move")
	}
	return c.batch(ctx, cmd, nil, append(append([]string(nil), sources...), target))
}

// Trash moves nodes to the trash. Paths must all be in the same section kind.
func (c *Client) Trash(ctx context.Context, paths ...string) ([]drive.ItemResult, error) {
	const cmd = "filesystem trash"
	if err := checkRemote(cmd, paths...); err != nil {
		return nil, err
	}
	for _, p := range paths {
		if strings.HasPrefix(p, drive.SectionTrash.Root()) {
			return nil, usageErr(cmd, "item is already in the trash")
		}
	}
	return c.batch(ctx, cmd, nil, paths)
}

func checkTrashPaths(cmd string, paths []string) error {
	if err := checkRemote(cmd, paths...); err != nil {
		return err
	}
	for _, p := range paths {
		segs := drive.SplitSegments(p)
		if len(segs) != 3 || segs[1] != string(drive.SectionTrash) || segs[2] == "" {
			return usageErr(cmd, "only top-level trash items can be restored or deleted")
		}
	}
	return nil
}

// Restore restores trashed nodes, addressed as /trash/<name>.
func (c *Client) Restore(ctx context.Context, paths ...string) ([]drive.ItemResult, error) {
	const cmd = "filesystem restore"
	if err := checkTrashPaths(cmd, paths); err != nil {
		return nil, err
	}
	return c.batch(ctx, cmd, nil, paths)
}

// DeleteForever permanently deletes trashed nodes, addressed as /trash/<name>.
func (c *Client) DeleteForever(ctx context.Context, paths ...string) ([]drive.ItemResult, error) {
	const cmd = "filesystem delete"
	if err := checkTrashPaths(cmd, paths); err != nil {
		return nil, err
	}
	return c.batch(ctx, cmd, nil, paths)
}

// EmptyTrash permanently deletes everything in /trash (not /photos-trash).
func (c *Client) EmptyTrash(ctx context.Context) error {
	_, err := c.meta(ctx, 0, argv("filesystem", "empty-trash", nil))
	return err
}

// ----- transfers -----

// Conflict strategies: upload and download accept different sets (CONTRACT.md).
type (
	// UploadFileStrategy resolves a file name conflict during upload.
	UploadFileStrategy string
	// UploadFolderStrategy resolves a folder name conflict during upload.
	UploadFolderStrategy string
	// DownloadFileStrategy resolves a local file conflict during download.
	DownloadFileStrategy string
	// DownloadFolderStrategy resolves a local folder conflict during download.
	DownloadFolderStrategy string
)

// Strategy values. The non-destructive ones are the defaults in Quark.
const (
	UploadFileNewRevision UploadFileStrategy = "create-new-revision"
	UploadFileRename      UploadFileStrategy = "rename"
	UploadFileReplace     UploadFileStrategy = "replace" // trashes the remote item
	UploadFileSkip        UploadFileStrategy = "skip"

	UploadFolderMerge   UploadFolderStrategy = "merge"
	UploadFolderRename  UploadFolderStrategy = "rename"
	UploadFolderReplace UploadFolderStrategy = "replace" // trashes the remote folder
	UploadFolderSkip    UploadFolderStrategy = "skip"

	DownloadFileRename DownloadFileStrategy = "rename"
	DownloadFileRemove DownloadFileStrategy = "remove" // deletes the local file
	DownloadFileSkip   DownloadFileStrategy = "skip"

	DownloadFolderMerge  DownloadFolderStrategy = "merge"
	DownloadFolderRename DownloadFolderStrategy = "rename"
	DownloadFolderRemove DownloadFolderStrategy = "remove" // deletes the local folder
	DownloadFolderSkip   DownloadFolderStrategy = "skip"
)

func oneOf[T ~string](v T, allowed ...T) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

// UploadSpec describes an upload.
type UploadSpec struct {
	LocalPaths     []string // absolute files or folders
	Parent         string   // remote folder
	FileStrategy   UploadFileStrategy
	FolderStrategy UploadFolderStrategy
	SkipThumbnails bool
}

// Upload runs one upload to completion or cancellation. On partial failure it
// returns the summary and a KindPartial error.
func (c *Client) Upload(ctx context.Context, s UploadSpec) (drive.TransferSummary, error) {
	const cmd = "filesystem upload"
	if err := checkLocal(cmd, s.LocalPaths...); err != nil {
		return drive.TransferSummary{}, err
	}
	if err := checkRemote(cmd, s.Parent); err != nil {
		return drive.TransferSummary{}, err
	}
	if !oneOf(s.FileStrategy, UploadFileNewRevision, UploadFileRename, UploadFileReplace, UploadFileSkip) ||
		!oneOf(s.FolderStrategy, UploadFolderMerge, UploadFolderRename, UploadFolderReplace, UploadFolderSkip) {
		return drive.TransferSummary{}, usageErr(cmd, "conflict strategies must be set explicitly")
	}
	opts := []string{"--file-conflict-strategy", string(s.FileStrategy), "--folder-conflict-strategy", string(s.FolderStrategy)}
	if s.SkipThumbnails {
		opts = append(opts, "--skip-thumbnails")
	}
	return c.transfer(ctx, cmd, argv("filesystem", "upload", opts, append(append([]string(nil), s.LocalPaths...), s.Parent)...))
}

// DownloadSpec describes a download.
type DownloadSpec struct {
	Remotes        []string
	LocalDir       string
	FileStrategy   DownloadFileStrategy
	FolderStrategy DownloadFolderStrategy
}

// Download runs one download to completion or cancellation.
func (c *Client) Download(ctx context.Context, s DownloadSpec) (drive.TransferSummary, error) {
	const cmd = "filesystem download"
	if err := checkRemote(cmd, s.Remotes...); err != nil {
		return drive.TransferSummary{}, err
	}
	if err := checkLocal(cmd, s.LocalDir); err != nil {
		return drive.TransferSummary{}, err
	}
	if !oneOf(s.FileStrategy, DownloadFileRename, DownloadFileRemove, DownloadFileSkip) ||
		!oneOf(s.FolderStrategy, DownloadFolderMerge, DownloadFolderRename, DownloadFolderRemove, DownloadFolderSkip) {
		return drive.TransferSummary{}, usageErr(cmd, "conflict strategies must be set explicitly")
	}
	opts := []string{"--file-conflict-strategy", string(s.FileStrategy), "--folder-conflict-strategy", string(s.FolderStrategy)}
	return c.transfer(ctx, cmd, argv("filesystem", "download", opts, append(append([]string(nil), s.Remotes...), s.LocalDir)...))
}

func (c *Client) transfer(ctx context.Context, command string, args []string) (drive.TransferSummary, error) {
	// No timeout: transfers can legitimately take hours; ctx cancels them.
	res, err := c.runner.Run(ctx, Request{Args: args})
	if err != nil {
		if IsKind(err, KindPartial) {
			if s, perr := ParseSummary(command, res.Stdout); perr == nil {
				return s, err
			}
		}
		return drive.TransferSummary{}, err
	}
	return ParseSummary(command, res.Stdout)
}

// ----- sharing -----

// Sharing returns the sharing state of a node, or nil if it is not shared.
func (c *Client) Sharing(ctx context.Context, path string) (*drive.Sharing, error) {
	const cmd = "sharing status"
	if err := checkRemote(cmd, path); err != nil {
		return nil, err
	}
	out, err := c.meta(ctx, 0, argv("sharing", "status", nil, path))
	if err != nil {
		return nil, err
	}
	return ParseSharing(cmd, out)
}

var emailPattern = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

func checkEmails(cmd string, emails []string) error {
	if len(emails) == 0 {
		return usageErr(cmd, "no email address given")
	}
	for _, e := range emails {
		if !emailPattern.MatchString(e) {
			return usageErr(cmd, "invalid email address")
		}
	}
	return nil
}

// InviteSpec describes a sharing invitation.
type InviteSpec struct {
	Emails []string
	Role   drive.Role // viewer, editor or admin
	// Message and IncludeName are sent in clear text in the invitation email.
	Message     string
	IncludeName bool
}

// Invite shares a node with users by email.
func (c *Client) Invite(ctx context.Context, path string, s InviteSpec) (*drive.Sharing, error) {
	const cmd = "sharing invite"
	if err := checkRemote(cmd, path); err != nil {
		return nil, err
	}
	if err := checkEmails(cmd, s.Emails); err != nil {
		return nil, err
	}
	if !oneOf(s.Role, drive.RoleViewer, drive.RoleEditor, drive.RoleAdmin) {
		return nil, usageErr(cmd, "invalid role")
	}
	var opts []string
	for _, e := range s.Emails {
		opts = append(opts, "--user", e)
	}
	opts = append(opts, "--role", string(s.Role))
	if s.Message != "" {
		opts = append(opts, "--message", s.Message)
	}
	if s.IncludeName {
		opts = append(opts, "--include-node-name")
	}
	out, err := c.meta(ctx, 0, argv("sharing", "invite", opts, path))
	if err != nil {
		return nil, err
	}
	return ParseSharing(cmd, out)
}

// RemoveMembers removes access or pending invitations for the given emails.
func (c *Client) RemoveMembers(ctx context.Context, path string, emails []string) (*drive.Sharing, error) {
	const cmd = "sharing remove"
	if err := checkRemote(cmd, path); err != nil {
		return nil, err
	}
	if err := checkEmails(cmd, emails); err != nil {
		return nil, err
	}
	var opts []string
	for _, e := range emails {
		opts = append(opts, "--email", e)
	}
	out, err := c.meta(ctx, 0, argv("sharing", "remove", opts, path))
	if err != nil {
		return nil, err
	}
	return ParseSharing(cmd, out)
}

// LinkSpec describes a public link.
type LinkSpec struct {
	Role drive.Role // viewer or editor
	// Password is optional. Note: the CLI only accepts it as an argument, so it
	// is briefly visible in the process list to other local users.
	Password string
	Expires  time.Time // zero for no expiry
}

// SetLink creates or updates the public link of a node.
func (c *Client) SetLink(ctx context.Context, path string, s LinkSpec) (*drive.Sharing, error) {
	const cmd = "sharing set-url"
	if err := checkRemote(cmd, path); err != nil {
		return nil, err
	}
	if !oneOf(s.Role, drive.RoleViewer, drive.RoleEditor) {
		return nil, usageErr(cmd, "invalid role")
	}
	opts := []string{"--role", string(s.Role)}
	if s.Password != "" {
		opts = append(opts, "--password", s.Password)
	}
	if !s.Expires.IsZero() {
		opts = append(opts, "--expiration", s.Expires.UTC().Format("2006-01-02"))
	}
	out, err := c.meta(ctx, 0, argv("sharing", "set-url", opts, path))
	if err != nil {
		return nil, err
	}
	return ParseSharing(cmd, out)
}

// RemoveLink removes the public link; direct members keep access.
func (c *Client) RemoveLink(ctx context.Context, path string) (*drive.Sharing, error) {
	const cmd = "sharing remove-url"
	if err := checkRemote(cmd, path); err != nil {
		return nil, err
	}
	out, err := c.meta(ctx, 0, argv("sharing", "remove-url", nil, path))
	if err != nil {
		return nil, err
	}
	return ParseSharing(cmd, out)
}

// Leave leaves a node shared with the current user.
func (c *Client) Leave(ctx context.Context, path string) error {
	if err := checkRemote("sharing leave", path); err != nil {
		return err
	}
	_, err := c.meta(ctx, 0, argv("sharing", "leave", nil, path))
	return err
}

// Invitations lists pending invitations for the current user.
func (c *Client) Invitations(ctx context.Context) ([]drive.Invitation, error) {
	out, err := c.meta(ctx, 0, argv("invitation", "list", nil))
	if err != nil {
		return nil, err
	}
	return ParseInvitations(out)
}

func checkInvitationUID(cmd, uid string) error {
	if (!strings.HasPrefix(uid, "drive~") && !strings.HasPrefix(uid, "photos~")) || strings.ContainsAny(uid, " \x00") {
		return usageErr(cmd, "invalid invitation id")
	}
	return nil
}

// AcceptInvitation accepts a pending invitation.
func (c *Client) AcceptInvitation(ctx context.Context, uid string) error {
	if err := checkInvitationUID("invitation accept", uid); err != nil {
		return err
	}
	_, err := c.meta(ctx, 0, argv("invitation", "accept", nil, uid))
	return err
}

// RejectInvitation rejects a pending invitation.
func (c *Client) RejectInvitation(ctx context.Context, uid string) error {
	if err := checkInvitationUID("invitation reject", uid); err != nil {
		return err
	}
	_, err := c.meta(ctx, 0, argv("invitation", "reject", nil, uid))
	return err
}

// ----- auth -----

// Login runs the browser sign-in. onURL is called once with the validated
// sign-in URL as soon as the CLI prints it (the CLI also opens the browser
// itself). onURL runs on an output goroutine and must not block. Login
// returns when sign-in completes, fails, times out or ctx is cancelled.
func (c *Client) Login(ctx context.Context, onURL func(string)) error {
	sent := false
	_, err := c.runner.Run(ctx, Request{
		Args:    []string{"auth", "login", "--json"},
		Timeout: c.LoginTimeout,
		OnStdoutLine: func(line string) {
			if u, ok := ParseSignInURL(line); ok && !sent {
				sent = true
				onURL(u)
			}
		},
	})
	return err
}

// Logout signs out and clears the CLI's credentials and caches.
func (c *Client) Logout(ctx context.Context) error {
	_, err := c.meta(ctx, 0, []string{"auth", "logout", "--json"})
	return err
}

// ----- version -----

// Version is a parsed CLI version.
type Version struct {
	Major, Minor, Patch int
	Raw                 string // e.g. "cli-drive@0.8.0+06e8c605"
	SDK                 string
	// Newer is set when the CLI reported a newer release.
	Newer string
}

func (v Version) String() string { return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch) }

// Compare returns -1, 0 or 1.
func (v Version) Compare(o Version) int {
	for _, d := range [3]int{v.Major - o.Major, v.Minor - o.Minor, v.Patch - o.Patch} {
		if d != 0 {
			if d < 0 {
				return -1
			}
			return 1
		}
	}
	return 0
}

// Supported CLI versions. Minimum is enforced; versions above the newest
// tested one run with a warning until fixtures are re-captured.
var (
	MinVersion    = Version{Major: 0, Minor: 8, Patch: 0}
	TestedVersion = Version{Major: 0, Minor: 8, Patch: 0}
)

var (
	cliVersionLine = regexp.MustCompile(`Proton Drive CLI (\S*@(\d+)\.(\d+)\.(\d+)\S*)`)
	sdkVersionLine = regexp.MustCompile(`Proton Drive SDK (\S+)`)
	newerLine      = regexp.MustCompile(`A newer version is available: (\d+\.\d+\.\d+)`)
)

// ParseVersion parses `proton-drive --version` output.
func ParseVersion(out []byte) (Version, error) {
	m := cliVersionLine.FindSubmatch(out)
	if m == nil {
		return Version{}, badOutput("--version", out, errors.New("no version line"))
	}
	v := Version{Raw: string(m[1])}
	v.Major, _ = strconv.Atoi(string(m[2]))
	v.Minor, _ = strconv.Atoi(string(m[3]))
	v.Patch, _ = strconv.Atoi(string(m[4]))
	if s := sdkVersionLine.FindSubmatch(out); s != nil {
		v.SDK = string(s[1])
	}
	if n := newerLine.FindSubmatch(out); n != nil {
		v.Newer = string(n[1])
	}
	return v, nil
}

// Version runs `--version` and checks the result against MinVersion.
func (c *Client) Version(ctx context.Context) (Version, error) {
	// --version also queries proton.me for updates (5 s timeout in the CLI).
	res, err := c.runner.Run(ctx, Request{Args: []string{"--version"}, Timeout: 20 * time.Second})
	if err != nil {
		return Version{}, err
	}
	v, err := ParseVersion(res.Stdout)
	if err != nil {
		return Version{}, err
	}
	if v.Compare(MinVersion) < 0 {
		return v, &Error{Kind: KindIncompatibleVersion, Command: "--version", Message: fmt.Sprintf("Proton Drive CLI %s is too old; %s or newer is required", v, MinVersion)}
	}
	return v, nil
}

// Untested reports whether v is newer than the newest version Quark was tested with.
func (v Version) Untested() bool { return v.Compare(TestedVersion) > 0 }
