package service

import (
	"os"
	"sync"
)

// maxServiceLog is the size at which the Windows service's log is rotated.
const maxServiceLog = 10 << 20

// rotatingLog is an append-only log file that is moved to NAME.1 (replacing
// the previous one) when it would grow past max, at open and while
// writing. If the file cannot be renamed (a reader holds it open, which on
// Windows prevents a rename), it is truncated instead; `service logs -f`
// copes with that.
type rotatingLog struct {
	mu   sync.Mutex
	path string
	max  int64
	f    *os.File
	size int64
}

func openRotatingLog(path string, max int64) (*rotatingLog, error) {
	l := &rotatingLog{path: path, max: max}
	if fi, err := os.Stat(path); err == nil && fi.Size() > max {
		l.rotateFile()
	}
	if err := l.open(0); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *rotatingLog) open(extra int) error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND|extra, 0o600)
	if err != nil {
		return err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	l.f, l.size = f, fi.Size()
	return nil
}

// rotateFile moves the closed log aside; it reports whether that worked.
func (l *rotatingLog) rotateFile() bool {
	os.Remove(l.path + ".1")
	return os.Rename(l.path, l.path+".1") == nil
}

func (l *rotatingLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return 0, os.ErrClosed
	}
	if l.size > 0 && l.size+int64(len(p)) > l.max {
		l.f.Close()
		l.f = nil
		extra := 0
		if !l.rotateFile() {
			extra = os.O_TRUNC
		}
		if err := l.open(extra); err != nil {
			return 0, err
		}
	}
	n, err := l.f.Write(p)
	l.size += int64(n)
	return n, err
}

func (l *rotatingLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}
