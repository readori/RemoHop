package wake

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/readori/RemoHop/internal/config"
)

const (
	storeFile   = "wake.json"
	maxStoreLen = 1 << 20
	maxNames    = 1000
)

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`)

// Store remembers MAC addresses under short names, in a 0600 JSON file in the
// config directory.
type Store struct{ path string }

// OpenStore returns the store in the config directory.
func OpenStore() (*Store, error) {
	dir, err := config.Dir()
	if err != nil {
		return nil, err
	}
	return &Store{path: filepath.Join(dir, storeFile)}, nil
}

// ValidName reports whether name can be saved. Names that parse as MAC
// addresses are refused so `remohop wake X` is never ambiguous.
func ValidName(name string) error {
	if !nameRE.MatchString(name) {
		return fmt.Errorf("invalid name %q (1-32 letters, digits, '.', '_' or '-')", clip(name))
	}
	if _, err := ParseMAC(name); err == nil {
		return fmt.Errorf("name %q looks like a MAC address", name)
	}
	return nil
}

func (s *Store) load() (map[string]string, error) {
	f, err := os.Open(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	m := map[string]string{}
	dec := json.NewDecoder(&limitedReader{f: f, left: maxStoreLen})
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("%s: %w", s.path, err)
	}
	return m, nil
}

type limitedReader struct {
	f    *os.File
	left int
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.left <= 0 {
		return 0, errors.New("file too large")
	}
	if len(p) > l.left {
		p = p[:l.left]
	}
	n, err := l.f.Read(p)
	l.left -= n
	return n, err
}

func (s *Store) write(m map[string]string) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), storeFile+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}

// Save stores mac (normalised) under name, replacing any earlier entry.
func (s *Store) Save(name, mac string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	hw, err := ParseMAC(mac)
	if err != nil {
		return err
	}
	m, err := s.load()
	if err != nil {
		return err
	}
	if _, exists := m[name]; !exists && len(m) >= maxNames {
		return errors.New("too many saved names")
	}
	m[name] = hw.String()
	return s.write(m)
}

// Lookup returns the MAC saved under name.
func (s *Store) Lookup(name string) (string, bool, error) {
	m, err := s.load()
	if err != nil {
		return "", false, err
	}
	mac, ok := m[name]
	return mac, ok, nil
}

// Forget removes name; it reports whether it existed.
func (s *Store) Forget(name string) (bool, error) {
	m, err := s.load()
	if err != nil {
		return false, err
	}
	if _, ok := m[name]; !ok {
		return false, nil
	}
	delete(m, name)
	return true, s.write(m)
}

// Entry is one saved name.
type Entry struct {
	Name string `json:"name"`
	MAC  string `json:"mac"`
}

// List returns the saved names in alphabetical order.
func (s *Store) List() ([]Entry, error) {
	m, err := s.load()
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(m))
	for n, mac := range m {
		out = append(out, Entry{n, mac})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
