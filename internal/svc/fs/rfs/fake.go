package rfs

import (
	"encoding/json"

	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/wire"
)

// FakeFunc answers one request of a synthetic host. op is "stat", "lstat",
// "readdir", "readfile", "realpath" or "hash"; it returns attributes (stat,
// readfile), directory entries (readdir), data (readfile, or the string
// result of realpath/hash), or a POSIX error name. A readfile answer with
// attributes but nil data says the file is too big for one round trip.
type FakeFunc func(op, path string) (attr *Attr, entries []Attr, data []byte, errCode string)

// ServeFake serves the read-only part of the control protocol on s from
// fn ("info" gets a fixed reply), answering every other operation with
// ENOSYS. It lets other packages test their client code against synthetic
// or deliberately hostile hosts (for example listings with "../x" names, or
// directories aliasing their ancestors) over the real wire format.
func ServeFake(s peer.Stream, fn FakeFunc) error {
	ops := map[byte]string{
		opStat: "stat", opLstat: "lstat", opOpenDir: "readdir", opReadFile: "readfile",
		opRealpath: "realpath", opHash: "hash",
	}
	for {
		typ, payload, err := wire.ReadFrame(s, nil)
		if err != nil {
			return nil
		}
		id, meta, _, err := parseMsg(payload)
		if err != nil {
			return err
		}
		var req request
		json.Unmarshal(meta, &req)
		rep := reply{}
		var data []byte
		if typ == opInfo {
			rep.Info = &Info{Home: "/", OS: "fake"}
		} else if op, ok := ops[typ]; !ok {
			rep.Code = "ENOSYS"
		} else {
			a, ents, d, code := fn(op, req.Path)
			rep.Code = code
			switch op {
			case "readdir":
				rep.Entries, rep.EOF = ents, true
			case "realpath", "hash":
				rep.Str = string(d)
			case "readfile":
				// Attributes without data announce a file too big
				// for one round trip (the client then uses fs.get).
				rep.Attr, data = a, d
				rep.TooBig = a != nil && d == nil && code == ""
			default:
				rep.Attr = a
			}
		}
		st := stOK
		if rep.Code != "" {
			st = stErr
		}
		meta, _ = json.Marshal(rep)
		if _, err := s.Write(appendMsg(st, id, meta, data)); err != nil {
			return err
		}
	}
}
