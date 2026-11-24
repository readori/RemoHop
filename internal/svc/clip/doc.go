// Package clip is the RemoHop clipboard service: two-way sync of text and
// images between the local and the remote machine, one-shot get/set, and a
// client-side history that can be exported as Markdown for LLMs.
//
// # Clipboard backends
//
// A Board is the abstraction over "the system clipboard". Backends are
// pure Go plus external helpers (no cgo) and are chosen at runtime by Detect:
// wl-clipboard on Wayland, xclip or xsel on X11, pbcopy/osascript on macOS,
// PowerShell on Windows. When no system clipboard is reachable (a typical
// server) an in-memory virtual clipboard owned by the process is used, so
// get/set and sync still work against a headless host.
//
// # Wire protocol
//
// Service "clip". The opener's request is {"op": "info"|"get"|"set"|"sync"}
// (sync also carries push/pull/images). Clipboard items travel as a JSON
// message {"item": {kind, size, hash, ts}} followed by ceil(size/chunk) binary
// frames of type 'D' holding the data. Receivers derive the MIME type from the
// kind, enforce MaxSize before reading, recompute the hash and validate PNGs,
// so neither end trusts the other's metadata.
//
//   - info: host answers {"backend": ...}.
//   - get:  host answers {"backend", "empty"} plus an item unless empty.
//   - set:  opener sends an item, host answers {"backend"} or {"error"}.
//   - history: {"h": "list", "limit"} answers {"entries": [...]} with the
//     newest entries of the host's own history (at most MaxRemoteList, text as
//     a short preview, no host paths); {"h": "get", "id"} answers the entry
//     followed by its content as an item; {"h": "restore", "id"} puts the entry
//     back on the host clipboard and answers the entry.
//   - sync: both sides first exchange a hello message carrying the backend
//     name, then each side streams items for its local changes and applies
//     the other's.
//
// # Sync semantics
//
// Only changes made after the sync starts are transferred; whatever is on
// either clipboard at start stays put. Echo loops are prevented by remembering
// the hash of the last content that was either sent or applied: the clipboard
// change caused by applying a remote item is recognised and not sent back.
//
// When both sides change at the same time, the later writer wins on both ends
// (last writer wins, not a merge). "Later" is decided by a Lamport counter
// (incremented per local change, raised to the highest value seen from the
// peer), never by wall-clock time, so clock skew between the machines cannot
// make a stale change beat a newer one that followed it. Exact ties go to the
// host. Both ends apply the same rule to the same two items, so they converge.
package clip
