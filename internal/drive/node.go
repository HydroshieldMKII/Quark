package drive

import "time"

// NodeType is the kind of a node.
type NodeType string

// Node types reported by the CLI.
const (
	TypeFile   NodeType = "file"
	TypeFolder NodeType = "folder"
	TypeAlbum  NodeType = "album"
	TypePhoto  NodeType = "photo"
)

// Role is a sharing role.
type Role string

// Roles reported by the CLI.
const (
	RoleViewer    Role = "viewer"
	RoleEditor    Role = "editor"
	RoleAdmin     Role = "admin"
	RoleInherited Role = "inherited"
)

// Node is Quark's view of a file or folder.
type Node struct {
	UID       string
	ParentUID string
	// Name is the display name: the decrypted name, a placeholder for invalid
	// names, or the UID when the name cannot be decrypted (same rule as the CLI).
	Name string
	// NameOK is false when the name could not be decrypted or is invalid.
	NameOK    bool
	Type      NodeType
	MediaType string
	// Size is the plaintext size claimed by the uploader; HasSize is false when unknown.
	Size    int64
	HasSize bool
	// StorageSize is the encrypted size of all revisions on the server.
	StorageSize int64
	Created     time.Time
	Modified    time.Time
	// ContentModified is the file's own modification time, when known.
	ContentModified time.Time
	Trashed         time.Time
	IsShared        bool
	IsSharedByURL   bool
	DirectRole      Role
	OwnerEmail      string
	// Author is the key author's address, or the claimed address when unverified.
	Author         string
	AuthorVerified bool
	SHA1           string
	// Degraded is true when the CLI reported decryption or integrity problems.
	Degraded bool
}

// IsFolder reports whether the node can contain children.
func (n Node) IsFolder() bool { return n.Type == TypeFolder }

// ItemResult is the outcome for one node of a batch operation (trash, move, ...).
// The CLI does not report the failure reason in JSON mode.
type ItemResult struct {
	UID    string
	NewUID string // set by copy
	OK     bool
}

// TransferSummary is the final report of an upload or download.
type TransferSummary struct {
	TransferredItems int
	TransferredBytes int64
	SkippedItems     int
	FailedItems      int
	Failures         []TransferFailure
}

// TransferFailure describes one failed item of a transfer.
type TransferFailure struct {
	Name    string
	NodeUID string
	Error   string
}

// Member is a user with access, or a pending invitation, on a shared node.
type Member struct {
	UID             string
	Email           string
	Role            Role
	Invited         time.Time
	AddedBy         string
	AddedByVerified bool
	// State is set for invitations to non-Proton addresses ("pending", "userRegistered").
	State string
}

// PublicLink is the URL access of a shared node.
type PublicLink struct {
	UID               string
	URL               string
	Role              Role
	Created           time.Time
	Expires           time.Time // zero when no expiry
	HasCustomPassword bool
	// Password is the custom password, kept so that updates can re-send it:
	// the CLI replaces all link settings on every update. Never log it.
	Password  string
	Downloads int
}

// Sharing is the sharing state of a node. A nil *Sharing means not shared.
type Sharing struct {
	Members              []Member
	ProtonInvitations    []Member
	NonProtonInvitations []Member
	Link                 *PublicLink
	EditorsCanShare      bool
}

// Invitation is a pending invitation for the current user.
type Invitation struct {
	UID          string // "drive~..." or "photos~..."
	Role         Role
	Invited      time.Time
	From         string
	FromVerified bool
	NodeUID      string
	NodeName     string
	NodeType     NodeType
}

// Device is a computer synced to Proton Drive.
type Device struct {
	UID           string
	Name          string
	Type          string
	RootFolderUID string
	Created       time.Time
	LastSync      time.Time
}
