package pdcli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"example.org/quark/internal/drive"
)

// Wire types mirror the CLI's JSON (CONTRACT.md). They are decoded leniently:
// unknown fields are ignored and the Result-shaped fields accept a plain value
// too, so that small upstream changes degrade gracefully instead of failing.

type wireResultString struct {
	ok    bool
	value string
	// placeholder is InvalidNameError.name, a display substitute.
	placeholder string
	isNull      bool
}

func (r *wireResultString) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '"' {
		r.ok = true
		return json.Unmarshal(b, &r.value)
	}
	if bytes.Equal(b, []byte("null")) {
		r.ok, r.isNull = true, true
		return nil
	}
	var raw struct {
		OK    bool            `json:"ok"`
		Value json.RawMessage `json:"value"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	r.ok = raw.OK
	if raw.OK {
		v := bytes.TrimSpace(raw.Value)
		if len(v) == 0 || bytes.Equal(v, []byte("null")) {
			r.isNull = true
			return nil
		}
		return json.Unmarshal(v, &r.value)
	}
	// Error forms: {} (JS Error), {name, error} (InvalidNameError),
	// {claimedAuthor, error} (UnverifiedAuthorError). Tolerate anything else.
	var e struct {
		Name          *string `json:"name"`
		ClaimedAuthor *string `json:"claimedAuthor"`
	}
	if json.Unmarshal(raw.Error, &e) == nil {
		switch {
		case e.Name != nil:
			r.placeholder = *e.Name
		case e.ClaimedAuthor != nil:
			r.value = *e.ClaimedAuthor
		}
	}
	return nil
}

type wireRevision struct {
	UID                     string     `json:"uid"`
	StorageSize             int64      `json:"storageSize"`
	ClaimedSize             *int64     `json:"claimedSize"`
	ClaimedModificationTime *time.Time `json:"claimedModificationTime"`
	ClaimedDigests          *struct {
		SHA1 string `json:"sha1"`
	} `json:"claimedDigests"`
}

type wireNode struct {
	UID        string           `json:"uid"`
	ParentUID  string           `json:"parentUid"`
	Name       wireResultString `json:"name"`
	KeyAuthor  wireResultString `json:"keyAuthor"`
	DirectRole string           `json:"directRole"`
	OwnedBy    struct {
		Email string `json:"email"`
	} `json:"ownedBy"`
	Type             string        `json:"type"`
	MediaType        string        `json:"mediaType"`
	IsShared         bool          `json:"isShared"`
	IsSharedByURL    bool          `json:"isSharedByUrl"`
	CreationTime     time.Time     `json:"creationTime"`
	ModificationTime time.Time     `json:"modificationTime"`
	TrashTime        *time.Time    `json:"trashTime"`
	TotalStorageSize *int64        `json:"totalStorageSize"`
	ActiveRevision   *wireRevision `json:"activeRevision"`
	Folder           *struct {
		ClaimedModificationTime *time.Time `json:"claimedModificationTime"`
	} `json:"folder"`
	Errors []json.RawMessage `json:"errors"`
}

func (w *wireNode) toNode() (drive.Node, error) {
	if w.UID == "" {
		return drive.Node{}, errors.New("node without uid")
	}
	n := drive.Node{
		UID:            w.UID,
		ParentUID:      w.ParentUID,
		Type:           drive.NodeType(w.Type),
		MediaType:      w.MediaType,
		IsShared:       w.IsShared,
		IsSharedByURL:  w.IsSharedByURL,
		DirectRole:     drive.Role(w.DirectRole),
		OwnerEmail:     w.OwnedBy.Email,
		Created:        w.CreationTime,
		Modified:       w.ModificationTime,
		Author:         w.KeyAuthor.value,
		AuthorVerified: w.KeyAuthor.ok,
		Degraded:       len(w.Errors) > 0,
	}
	switch {
	case w.Name.ok && w.Name.value != "":
		n.Name, n.NameOK = w.Name.value, true
	case w.Name.placeholder != "":
		n.Name = w.Name.placeholder
	default:
		n.Name = w.UID // same fallback as the CLI's getName
	}
	if w.TrashTime != nil {
		n.Trashed = *w.TrashTime
	}
	if w.TotalStorageSize != nil {
		n.StorageSize = *w.TotalStorageSize
	}
	if r := w.ActiveRevision; r != nil {
		if r.ClaimedSize != nil && *r.ClaimedSize >= 0 {
			n.Size, n.HasSize = *r.ClaimedSize, true
		}
		if r.ClaimedModificationTime != nil {
			n.ContentModified = *r.ClaimedModificationTime
		}
		if r.ClaimedDigests != nil {
			n.SHA1 = r.ClaimedDigests.SHA1
		}
	}
	if w.Folder != nil && w.Folder.ClaimedModificationTime != nil {
		n.ContentModified = *w.Folder.ClaimedModificationTime
	}
	return n, nil
}

func badOutput(command string, out []byte, err error) *Error {
	snippet := string(out)
	if len(snippet) > 512 {
		snippet = snippet[:512]
	}
	return &Error{Kind: KindBadOutput, Command: command, Message: "unexpected output from the Proton Drive CLI", Stderr: snippet, Err: err}
}

// decodeArray decodes the CLI's streamed JSON array. A missing closing bracket
// (the CLI failed mid-stream) is an error: a partial listing must never look complete.
func decodeArray[T any](command string, out []byte, each func(*T) error) error {
	dec := json.NewDecoder(bytes.NewReader(out))
	tok, err := dec.Token()
	if err != nil {
		return badOutput(command, out, fmt.Errorf("reading array start: %w", err))
	}
	if d, ok := tok.(json.Delim); !ok || d != '[' {
		return badOutput(command, out, errors.New("output is not a JSON array"))
	}
	for dec.More() {
		var v T
		if err := dec.Decode(&v); err != nil {
			return badOutput(command, out, fmt.Errorf("decoding item: %w", err))
		}
		if err := each(&v); err != nil {
			return badOutput(command, out, err)
		}
	}
	tok, err = dec.Token()
	if err != nil {
		return badOutput(command, out, fmt.Errorf("array not terminated (truncated output): %w", err))
	}
	if d, ok := tok.(json.Delim); !ok || d != ']' {
		return badOutput(command, out, errors.New("array not terminated"))
	}
	return expectEOF(command, out, dec)
}

// decodeObject decodes exactly one JSON value followed only by whitespace.
func decodeObject(command string, out []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(out))
	if err := dec.Decode(v); err != nil {
		return badOutput(command, out, err)
	}
	return expectEOF(command, out, dec)
}

func expectEOF(command string, out []byte, dec *json.Decoder) error {
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return badOutput(command, out, errors.New("trailing data after JSON"))
	}
	return nil
}

// ParseNodes parses `filesystem list` output for a folder or section.
func ParseNodes(out []byte) ([]drive.Node, error) {
	var nodes []drive.Node
	err := decodeArray("filesystem list", out, func(w *wireNode) error {
		n, err := w.toNode()
		if err != nil {
			return err
		}
		nodes = append(nodes, n)
		return nil
	})
	return nodes, err
}

// ParseNode parses one NodeEntity (`info`, `create-folder`, `rename`).
func ParseNode(command string, out []byte) (drive.Node, error) {
	var w wireNode
	if err := decodeObject(command, out, &w); err != nil {
		return drive.Node{}, err
	}
	n, err := w.toNode()
	if err != nil {
		return drive.Node{}, badOutput(command, out, err)
	}
	return n, nil
}

// ParseRoots parses `filesystem list /`.
func ParseRoots(out []byte) ([]string, error) {
	var roots []string
	err := decodeArray("filesystem list", out, func(w *struct {
		Path string `json:"path"`
	},
	) error {
		if !strings.HasPrefix(w.Path, "/") {
			return fmt.Errorf("bad root path %q", w.Path)
		}
		roots = append(roots, w.Path)
		return nil
	})
	return roots, err
}

type wireBatch struct {
	UID    string `json:"uid"`
	NewUID string `json:"newUid"`
	OK     bool   `json:"ok"`
}

// ParseBatch parses the per-item results of copy, move, trash, restore and delete.
func ParseBatch(command string, out []byte) ([]drive.ItemResult, error) {
	var res []drive.ItemResult
	err := decodeArray(command, out, func(w *wireBatch) error {
		if w.UID == "" {
			return errors.New("result without uid")
		}
		res = append(res, drive.ItemResult{UID: w.UID, NewUID: w.NewUID, OK: w.OK})
		return nil
	})
	return res, err
}

// ParseSummary parses the final line of upload and download.
func ParseSummary(command string, out []byte) (drive.TransferSummary, error) {
	var w struct {
		TransferredItems int   `json:"transferredItems"`
		TransferredBytes int64 `json:"transferredBytes"`
		SkippedItems     int   `json:"skippedItems"`
		FailedItems      int   `json:"failedItems"`
		Failures         []struct {
			Name    string `json:"name"`
			NodeUID string `json:"nodeUid"`
			Error   string `json:"error"`
		} `json:"failures"`
	}
	if err := decodeObject(command, out, &w); err != nil {
		return drive.TransferSummary{}, err
	}
	s := drive.TransferSummary{
		TransferredItems: w.TransferredItems, TransferredBytes: w.TransferredBytes,
		SkippedItems: w.SkippedItems, FailedItems: w.FailedItems,
	}
	for _, f := range w.Failures {
		s.Failures = append(s.Failures, drive.TransferFailure{Name: f.Name, NodeUID: f.NodeUID, Error: f.Error})
	}
	return s, nil
}

type wireMember struct {
	UID            string           `json:"uid"`
	InvitationTime time.Time        `json:"invitationTime"`
	AddedByEmail   wireResultString `json:"addedByEmail"`
	InviteeEmail   string           `json:"inviteeEmail"`
	Role           string           `json:"role"`
	State          string           `json:"state"`
}

func (w wireMember) toMember() drive.Member {
	return drive.Member{
		UID: w.UID, Email: w.InviteeEmail, Role: drive.Role(w.Role), Invited: w.InvitationTime,
		AddedBy: w.AddedByEmail.value, AddedByVerified: w.AddedByEmail.ok, State: w.State,
	}
}

// ParseSharing parses a ShareResult. The CLI prints the literal "undefined"
// for a node that is not shared, which yields (nil, nil).
func ParseSharing(command string, out []byte) (*drive.Sharing, error) {
	if string(bytes.TrimSpace(out)) == "undefined" {
		return nil, nil
	}
	var w struct {
		ProtonInvitations    []wireMember `json:"protonInvitations"`
		NonProtonInvitations []wireMember `json:"nonProtonInvitations"`
		Members              []wireMember `json:"members"`
		URLAccess            *struct {
			UID                          string     `json:"uid"`
			CreationTime                 time.Time  `json:"creationTime"`
			Role                         string     `json:"role"`
			URL                          string     `json:"url"`
			CustomPassword               *string    `json:"customPassword"`
			ExpirationTime               *time.Time `json:"expirationTime"`
			NumberOfInitializedDownloads int        `json:"numberOfInitializedDownloads"`
		} `json:"urlAccess"`
		EditorsCanShare bool `json:"editorsCanShare"`
	}
	if err := decodeObject(command, out, &w); err != nil {
		return nil, err
	}
	s := &drive.Sharing{EditorsCanShare: w.EditorsCanShare}
	for _, m := range w.Members {
		s.Members = append(s.Members, m.toMember())
	}
	for _, m := range w.ProtonInvitations {
		s.ProtonInvitations = append(s.ProtonInvitations, m.toMember())
	}
	for _, m := range w.NonProtonInvitations {
		s.NonProtonInvitations = append(s.NonProtonInvitations, m.toMember())
	}
	if u := w.URLAccess; u != nil {
		l := &drive.PublicLink{
			UID: u.UID, URL: u.URL, Role: drive.Role(u.Role), Created: u.CreationTime,
			HasCustomPassword: u.CustomPassword != nil && *u.CustomPassword != "",
			Downloads:         u.NumberOfInitializedDownloads,
		}
		if u.ExpirationTime != nil {
			l.Expires = *u.ExpirationTime
		}
		s.Link = l
	}
	return s, nil
}

// ParseInvitations parses `invitation list`.
func ParseInvitations(out []byte) ([]drive.Invitation, error) {
	var res []drive.Invitation
	err := decodeArray("invitation list", out, func(w *struct {
		wireMember
		Node struct {
			UID  string           `json:"uid"`
			Name wireResultString `json:"name"`
			Type string           `json:"type"`
		} `json:"node"`
	},
	) error {
		if !strings.HasPrefix(w.UID, "drive~") && !strings.HasPrefix(w.UID, "photos~") {
			return fmt.Errorf("unexpected invitation uid %q", w.UID)
		}
		name := w.Node.Name.value
		if !w.Node.Name.ok || name == "" {
			name = w.Node.Name.placeholder
		}
		res = append(res, drive.Invitation{
			UID: w.UID, Role: drive.Role(w.Role), Invited: w.InvitationTime,
			From: w.AddedByEmail.value, FromVerified: w.AddedByEmail.ok,
			NodeUID: w.Node.UID, NodeName: name, NodeType: drive.NodeType(w.Node.Type),
		})
		return nil
	})
	return res, err
}

// ParseDevices parses `filesystem list /devices`.
func ParseDevices(out []byte) ([]drive.Device, error) {
	var res []drive.Device
	err := decodeArray("filesystem list", out, func(w *struct {
		UID           string           `json:"uid"`
		Type          string           `json:"type"`
		Name          wireResultString `json:"name"`
		RootFolderUID string           `json:"rootFolderUid"`
		CreationTime  time.Time        `json:"creationTime"`
		LastSyncTime  *time.Time       `json:"lastSyncTime"`
	},
	) error {
		d := drive.Device{UID: w.UID, Type: w.Type, RootFolderUID: w.RootFolderUID, Created: w.CreationTime, Name: w.Name.value}
		if d.Name == "" {
			d.Name = w.Name.placeholder
		}
		if w.LastSyncTime != nil {
			d.LastSync = *w.LastSyncTime
		}
		res = append(res, d)
		return nil
	})
	return res, err
}

// ParseSignInURL extracts and validates the login URL printed by `auth login --json`.
// Only https URLs on Proton's account hosts are accepted.
func ParseSignInURL(line string) (string, bool) {
	var w struct {
		SignInURL string `json:"signInUrl"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(line)), &w) != nil || w.SignInURL == "" {
		return "", false
	}
	u, err := url.Parse(w.SignInURL)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return "", false
	}
	host := u.Hostname()
	if host != "account.proton.me" && host != "account.proton.black" {
		return "", false
	}
	return u.String(), true
}
