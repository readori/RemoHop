// Package fs is the RemoHop fs service: remote file access, transfer and
// mounting. Importing it registers everything:
//
//   - the host services "fs", "fs.get" and "fs.put" (package rfs), which
//     expose the host user's filesystem;
//   - `remohop <ID> cp` and `remohop <ID> sync` (package xfer);
//   - `remohop <ID> mount` and `remohop umount` (package mount).
//
// The Go API lives in those packages; the common entry points are
// re-exported here so callers such as the MCP server need only this
// import.
package fs

import (
	"context"

	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/svc/fs/mount"
	"github.com/readori/RemoHop/internal/svc/fs/rfs"
	"github.com/readori/RemoHop/internal/svc/fs/xfer"
)

type (
	// Client is a remote filesystem session (see rfs.Client).
	Client = rfs.Client
	// File is an open remote file.
	File = rfs.File
	// FileInfo describes a remote file; it implements io/fs.FileInfo.
	FileInfo = rfs.FileInfo
	// PutOptions control atomic remote writes.
	PutOptions = rfs.PutOptions
	// Path is a local or (":"-prefixed) remote transfer path.
	Path = xfer.Path
	// CopyOptions, CopyResult, SyncOptions and SyncResult configure and
	// report Copy and Sync.
	CopyOptions = xfer.CopyOptions
	CopyResult  = xfer.CopyResult
	SyncOptions = xfer.SyncOptions
	SyncResult  = xfer.SyncResult
	// MountOptions configure mount.New (Linux and macOS only).
	MountOptions = mount.Options
)

// NewClient opens a filesystem session on conn.
func NewClient(ctx context.Context, conn *peer.Conn) (*Client, error) {
	return rfs.NewClient(ctx, conn)
}

// ParsePath parses a command-line transfer path (":remote" or local).
func ParsePath(s string) Path { return xfer.ParsePath(s) }

// Copy copies files with cp/scp semantics (see xfer.Copy).
func Copy(ctx context.Context, c *Client, srcs []Path, dst Path, o CopyOptions) (*CopyResult, error) {
	return xfer.Copy(ctx, c, srcs, dst, o)
}

// Sync mirrors one directory onto another across the session (see
// xfer.Sync).
func Sync(ctx context.Context, c *Client, src, dst Path, o SyncOptions) (*SyncResult, error) {
	return xfer.Sync(ctx, c, src, dst, o)
}
