// Package rfs is the remote filesystem protocol of the RemoHop fs service:
// the host handlers that expose the host user's filesystem and the Client
// that the CLI commands (cp, sync, mount) and the MCP server use.
//
// # Wire protocol
//
// Three services are registered on the host:
//
//   - "fs" is the control channel. A client opens one stream per Client and
//     pipelines requests over it. Every message is a wire binary frame whose
//     type byte is the opcode (requests) or a status (replies) and whose
//     payload is
//
//     [u32 request id][u32 meta length][meta JSON][raw data]
//
//     Metadata is JSON so the protocol stays easy to extend; bulk bytes
//     (read results, write payloads) travel raw after it, never base64. The
//     host serves up to maxInflight requests of a stream concurrently and
//     replies out of order, matched by id, so many goroutines (FUSE, a
//     parallel copy) share one stream without head-of-line blocking on slow
//     operations, and nothing on the hot path pays for stream setup.
//
//   - "fs.get" streams one file to the client: a header frame with its
//     attributes, data frames, and an end frame carrying the final status.
//
//   - "fs.put" streams one file to the host, which writes it to a temporary
//     file next to the destination and renames it into place only after the
//     client's end frame arrives, so an interrupted upload never leaves a
//     truncated file under the final name.
//
// The bulk streams exist so large transfers run as one flow-controlled byte
// stream instead of a request per chunk; small files go over the control
// channel (opReadFile/opWriteFile) to avoid a stream round trip per file.
//
// Errors cross the wire as POSIX error names ("ENOENT") and come back as
// *fs.PathError values wrapping the local syscall.Errno, so errors.Is(err,
// fs.ErrNotExist) and FUSE error mapping work on the client.
package rfs

import (
	"encoding/binary"
	"errors"
	"io/fs"
	"time"
)

// Service names registered on the host.
const (
	ServiceControl = "fs"
	ServiceGet     = "fs.get"
	ServicePut     = "fs.put"
)

// Limits. They bound memory on both ends; every length read from the wire is
// checked against them.
const (
	// MaxIO is the largest payload of one read or write request.
	MaxIO = 1 << 20
	// SmallFile is the size up to which ReadFile/WriteFile use the control
	// channel instead of a bulk stream.
	SmallFile = MaxIO
	// maxMeta bounds the JSON metadata of a request.
	maxMeta = 64 << 10
	// maxInflight is how many requests of one control stream the host
	// serves concurrently. Together with MaxIO it caps host memory per
	// stream at about 64 MiB.
	maxInflight = 64
	// maxHandles bounds open files and directories per control stream.
	maxHandles = 4096
	// dirBatch is how many entries one directory read returns. Even with
	// maximal names and symlink targets a batch stays far below
	// wire.MaxFrame.
	dirBatch = 1024
	// bulkChunk is the data frame size on the bulk streams.
	bulkChunk = 1 << 20
	// protoVersion is sent when opening the control stream.
	protoVersion = 1
)

// Opcodes (frame type of a request on the control stream).
const (
	opInfo byte = iota + 1
	opStat
	opLstat
	opOpenDir
	opReadDir
	opClose
	opOpen
	opRead
	opWrite
	opFsync
	opFstat
	opFtruncate
	opMkdir
	opRemove
	opRename
	opSymlink
	opReadlink
	opLink
	opChmod
	opTruncate
	opUtimes
	opStatFS
	opReadFile
	opWriteFile
	opHash
	opRealpath
	opUserDir
)

// Reply status (frame type of a reply).
const (
	stOK  byte = 0
	stErr byte = 1
)

// Frame types on the bulk streams.
const (
	bulkHeader byte = 'h'
	bulkData   byte = 'd'
	bulkEnd    byte = 'e'
)

// Remove kinds.
const (
	removeAny  = ""     // os.Remove: file or empty directory
	removeFile = "file" // unlink(2): fails with EISDIR on a directory
	removeDir  = "dir"  // rmdir(2)
	removeAll  = "all"  // os.RemoveAll
)

// Rename flags.
const (
	RenameNoReplace = 1 << 0 // fail with EEXIST if the destination exists
	RenameExchange  = 1 << 1 // atomically swap the two paths (Linux only)
)

// Portable open flags. os.O_* values differ between platforms, so they are
// translated at both ends.
const (
	wWronly = 1 << iota
	wRdwr
	wAppend
	wCreate
	wExcl
	wTrunc
	wSync
)

// request is the metadata of every control request. Fields unused by an
// opcode stay empty and are omitted from the JSON.
type request struct {
	Path     string `json:"p,omitempty"`
	Path2    string `json:"q,omitempty"` // rename/link destination, symlink path
	Handle   uint64 `json:"h,omitempty"`
	Off      int64  `json:"o,omitempty"`
	N        int    `json:"n,omitempty"`
	Flags    int    `json:"f,omitempty"`
	Mode     uint32 `json:"m,omitempty"`
	Size     int64  `json:"s,omitempty"`
	Atime    *int64 `json:"at,omitempty"` // ns since epoch; nil = leave unchanged
	Mtime    *int64 `json:"mt,omitempty"`
	All      bool   `json:"all,omitempty"`
	Kind     string `json:"k,omitempty"`
	NoFollow bool   `json:"nf,omitempty"`
	Sync     bool   `json:"sync,omitempty"`
	Unique   bool   `json:"uq,omitempty"`
}

// reply is the metadata of every control reply.
type reply struct {
	Attr    *Attr   `json:"a,omitempty"`
	Entries []Attr  `json:"e,omitempty"`
	Handle  uint64  `json:"h,omitempty"`
	EOF     bool    `json:"eof,omitempty"`
	N       int     `json:"n,omitempty"`
	Str     string  `json:"str,omitempty"`
	TooBig  bool    `json:"big,omitempty"`
	StatFS  *StatFS `json:"fs,omitempty"`
	Info    *Info   `json:"info,omitempty"`
	Code    string  `json:"code,omitempty"` // error name, e.g. "ENOENT"
	Message string  `json:"msg,omitempty"`  // detail for errors without a POSIX name
}

// openReq is the Conn.Open request of the control service.
type openReq struct {
	Version int `json:"v"`
}

// getReq is the Conn.Open request of fs.get.
type getReq struct {
	Path string `json:"p"`
	Off  int64  `json:"o,omitempty"`
}

// putReq is the Conn.Open request of fs.put.
type putReq struct {
	Path     string `json:"p"`
	Mode     uint32 `json:"m,omitempty"`
	Mtime    *int64 `json:"mt,omitempty"`
	NoFollow bool   `json:"nf,omitempty"`
	Sync     bool   `json:"sync,omitempty"`
	// Unique installs under the first free name of Path, "name (1).ext",
	// "name (2).ext", ..., never replacing anything.
	Unique bool `json:"uq,omitempty"`
	// Partial resumes into this file (next to Path, named
	// PartialPrefix(Path)+...): its first Off bytes are kept and the
	// stream supplies the rest. Unlike a temporary file it survives a
	// broken upload.
	Partial string `json:"pt,omitempty"`
	Off     int64  `json:"o,omitempty"`
}

// bulkStatus is the payload of a bulk end frame sent by the host.
type bulkStatus struct {
	Attr    *Attr  `json:"a,omitempty"`
	Code    string `json:"code,omitempty"`
	Message string `json:"msg,omitempty"`
}

// Attr is the wire form of a file's attributes. Mode is a Unix st_mode (file
// type and permission bits) on every platform; times are nanoseconds since
// the Unix epoch.
type Attr struct {
	Name   string `json:"n,omitempty"`
	Mode   uint32 `json:"m"`
	Size   int64  `json:"s,omitempty"`
	Mtime  int64  `json:"mt,omitempty"`
	Atime  int64  `json:"at,omitempty"`
	Ctime  int64  `json:"ct,omitempty"`
	Nlink  uint32 `json:"nl,omitempty"`
	Uid    uint32 `json:"u,omitempty"`
	Gid    uint32 `json:"g,omitempty"`
	Ino    uint64 `json:"i,omitempty"`
	Dev    uint64 `json:"d,omitempty"`
	Blocks int64  `json:"b,omitempty"`
	// Target is the link target of a symlink, filled in by Lstat and
	// ReadDir so walkers need no extra round trip.
	Target string `json:"l,omitempty"`
}

// StatFS describes the filesystem holding a path.
type StatFS struct {
	Blocks  uint64 `json:"blocks"`
	Bfree   uint64 `json:"bfree"`
	Bavail  uint64 `json:"bavail"`
	Files   uint64 `json:"files"`
	Ffree   uint64 `json:"ffree"`
	Bsize   uint32 `json:"bsize"`
	Frsize  uint32 `json:"frsize"`
	NameLen uint32 `json:"namelen"`
}

// Info describes the host side of the filesystem session.
type Info struct {
	Home string `json:"home"`
	OS   string `json:"os"`
	UID  uint32 `json:"uid"`
	GID  uint32 `json:"gid"`
	// Umask is the host process's file creation mask (0 when the host
	// does not report one).
	Umask uint32 `json:"umask"`
}

// Unix file type and mode bits, defined here because the wire format uses
// them on every platform.
const (
	sIFMT   = 0o170000
	sIFSOCK = 0o140000
	sIFLNK  = 0o120000
	sIFREG  = 0o100000
	sIFBLK  = 0o060000
	sIFDIR  = 0o040000
	sIFCHR  = 0o020000
	sIFIFO  = 0o010000
	sISUID  = 0o4000
	sISGID  = 0o2000
	sISVTX  = 0o1000
)

// ModeToUnix converts a Go file mode to a Unix st_mode.
func ModeToUnix(m fs.FileMode) uint32 {
	u := uint32(m.Perm())
	switch {
	case m&fs.ModeDir != 0:
		u |= sIFDIR
	case m&fs.ModeSymlink != 0:
		u |= sIFLNK
	case m&fs.ModeNamedPipe != 0:
		u |= sIFIFO
	case m&fs.ModeSocket != 0:
		u |= sIFSOCK
	case m&fs.ModeCharDevice != 0:
		u |= sIFCHR
	case m&fs.ModeDevice != 0:
		u |= sIFBLK
	default:
		u |= sIFREG
	}
	if m&fs.ModeSetuid != 0 {
		u |= sISUID
	}
	if m&fs.ModeSetgid != 0 {
		u |= sISGID
	}
	if m&fs.ModeSticky != 0 {
		u |= sISVTX
	}
	return u
}

// ModeFromUnix converts a Unix st_mode to a Go file mode.
func ModeFromUnix(u uint32) fs.FileMode {
	m := fs.FileMode(u & 0o777)
	switch u & sIFMT {
	case sIFDIR:
		m |= fs.ModeDir
	case sIFLNK:
		m |= fs.ModeSymlink
	case sIFIFO:
		m |= fs.ModeNamedPipe
	case sIFSOCK:
		m |= fs.ModeSocket
	case sIFCHR:
		m |= fs.ModeDevice | fs.ModeCharDevice
	case sIFBLK:
		m |= fs.ModeDevice
	}
	if u&sISUID != 0 {
		m |= fs.ModeSetuid
	}
	if u&sISGID != 0 {
		m |= fs.ModeSetgid
	}
	if u&sISVTX != 0 {
		m |= fs.ModeSticky
	}
	return m
}

// FileInfo is a remote file's attributes. It implements fs.FileInfo; Sys
// returns the raw *Attr.
type FileInfo struct{ a Attr }

func newInfo(a Attr) *FileInfo { return &FileInfo{a: a} }

// Name is the base name of the file.
func (fi *FileInfo) Name() string { return fi.a.Name }

// Size is the length in bytes.
func (fi *FileInfo) Size() int64 { return fi.a.Size }

// Mode is the Go file mode.
func (fi *FileInfo) Mode() fs.FileMode { return ModeFromUnix(fi.a.Mode) }

// ModTime is the modification time.
func (fi *FileInfo) ModTime() time.Time { return time.Unix(0, fi.a.Mtime) }

// IsDir reports whether the file is a directory.
func (fi *FileInfo) IsDir() bool { return fi.a.Mode&sIFMT == sIFDIR }

// Sys returns the *Attr.
func (fi *FileInfo) Sys() any { return &fi.a }

// Attr returns the raw wire attributes (Unix mode, uid/gid, inode, ...).
func (fi *FileInfo) Attr() *Attr { return &fi.a }

// Target is the link target of a symlink returned by Lstat or ReadDir.
func (fi *FileInfo) Target() string { return fi.a.Target }

// IsRegular reports whether the file is a regular file.
func (fi *FileInfo) IsRegular() bool { return fi.a.Mode&sIFMT == sIFREG }

// IsSymlink reports whether the file is a symbolic link.
func (fi *FileInfo) IsSymlink() bool { return fi.a.Mode&sIFMT == sIFLNK }

var errShort = errors.New("rfs: short frame")

// appendMsg encodes one control message as a complete wire frame
// ([u32 len][type] header included) so it can be written with one Write.
func appendMsg(typ byte, id uint32, meta, data []byte) []byte {
	n := 8 + len(meta) + len(data)
	buf := make([]byte, 5+n)
	binary.BigEndian.PutUint32(buf, uint32(n))
	buf[4] = typ
	binary.BigEndian.PutUint32(buf[5:], id)
	binary.BigEndian.PutUint32(buf[9:], uint32(len(meta)))
	copy(buf[13:], meta)
	copy(buf[13+len(meta):], data)
	return buf
}

// parseMsg splits a control frame payload.
func parseMsg(p []byte) (id uint32, meta, data []byte, err error) {
	if len(p) < 8 {
		return 0, nil, nil, errShort
	}
	id = binary.BigEndian.Uint32(p)
	ml := binary.BigEndian.Uint32(p[4:])
	if uint64(ml) > uint64(len(p)-8) {
		return 0, nil, nil, errShort
	}
	return id, p[8 : 8+ml], p[8+ml:], nil
}

// timePtr converts t to the wire's optional nanosecond timestamp.
func timePtr(t time.Time) *int64 {
	if t.IsZero() {
		return nil
	}
	ns := t.UnixNano()
	return &ns
}
