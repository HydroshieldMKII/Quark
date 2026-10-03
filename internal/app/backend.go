package app

import (
	"context"

	"example.org/quark/internal/drive"
	"example.org/quark/internal/pdcli"
)

// Backend is the subset of *pdcli.Client the application uses. Tests use a fake.
type Backend interface {
	Version(ctx context.Context) (pdcli.Version, error)
	Roots(ctx context.Context) ([]string, error)
	List(ctx context.Context, path string) ([]drive.Node, error)
	Devices(ctx context.Context) ([]drive.Device, error)
	Info(ctx context.Context, path string) (drive.Node, error)
	CreateFolder(ctx context.Context, parent, name string) (drive.Node, error)
	Rename(ctx context.Context, path, newName string) (drive.Node, error)
	Copy(ctx context.Context, sources []string, target, name string) ([]drive.ItemResult, error)
	Move(ctx context.Context, sources []string, target string) ([]drive.ItemResult, error)
	Trash(ctx context.Context, paths ...string) ([]drive.ItemResult, error)
	Restore(ctx context.Context, paths ...string) ([]drive.ItemResult, error)
	DeleteForever(ctx context.Context, paths ...string) ([]drive.ItemResult, error)
	EmptyTrash(ctx context.Context) error
	Login(ctx context.Context, onURL func(string)) error
	Logout(ctx context.Context) error
}

var _ Backend = (*pdcli.Client)(nil)
