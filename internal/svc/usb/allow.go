package usb

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/readori/RemoHop/internal/config"
)

const allowFile = "usb-allow.json"

// AllowEntry is one device the host owner agreed to export. The VID:PID is
// recorded with the busid so that a different device plugged into the same
// port later is not exported by accident.
type AllowEntry struct {
	BusID string `json:"busid"`
	VID   string `json:"vid"`
	PID   string `json:"pid"`
}

type allowDoc struct {
	Allowed []AllowEntry `json:"allowed"`
}

func allowPath() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, allowFile), nil
}

func loadAllow(path string) (allowDoc, error) {
	var doc allowDoc
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return doc, nil
	}
	if err != nil {
		return doc, err
	}
	if len(b) > 1<<20 {
		return doc, errors.New("usb allow list too large")
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return doc, fmt.Errorf("%s: %w", path, err)
	}
	return doc, nil
}

// Allowed lists the devices the host owner allowed.
func Allowed() ([]AllowEntry, error) {
	p, err := allowPath()
	if err != nil {
		return nil, err
	}
	doc, err := loadAllow(p)
	return doc.Allowed, err
}

// IsAllowed reports whether device busid with this VID:PID may be exported.
func IsAllowed(busid, vid, pid string) bool {
	list, err := Allowed()
	if err != nil {
		return false
	}
	for _, e := range list {
		if e.BusID == busid && e.VID == vid && e.PID == pid {
			return true
		}
	}
	return false
}

// Allow records e (replacing any entry for the same busid).
func Allow(e AllowEntry) error {
	return editAllow(func(l []AllowEntry) []AllowEntry {
		out := l[:0:0]
		for _, x := range l {
			if x.BusID != e.BusID {
				out = append(out, x)
			}
		}
		return append(out, e)
	})
}

// Disallow removes busid; it reports whether it was allowed.
func Disallow(busid string) (bool, error) {
	was := false
	err := editAllow(func(l []AllowEntry) []AllowEntry {
		out := l[:0:0]
		for _, x := range l {
			if x.BusID == busid {
				was = true
				continue
			}
			out = append(out, x)
		}
		return out
	})
	return was, err
}

func editAllow(f func([]AllowEntry) []AllowEntry) error {
	p, err := allowPath()
	if err != nil {
		return err
	}
	unlock, err := config.Lock(p)
	if err != nil {
		return err
	}
	defer unlock()
	doc, err := loadAllow(p)
	if err != nil {
		return err
	}
	doc.Allowed = f(doc.Allowed)
	sort.Slice(doc.Allowed, func(i, j int) bool { return busidLess(doc.Allowed[i].BusID, doc.Allowed[j].BusID) })
	b, _ := json.MarshalIndent(doc, "", "  ")
	return config.WriteFileAtomic(p, append(b, '\n'), 0o600)
}
