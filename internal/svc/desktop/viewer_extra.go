package desktop

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/readori/RemoHop/internal/svc/fs/rfs"
)

// The viewer's audio bridge and file upload, kept apart from the video
// bridge in viewer.go.

// Binary audio message to the page: 'A', sequence (4), capture time on the
// local clock (8, microseconds), arrival time on the local clock (8), then
// one Opus packet.
const audioPageHeaderLen = 21

// audioBridge forwards the host's audio to the page while the page wants it.
type audioBridge struct {
	v        *viewer
	ctx      context.Context
	sess     *Session
	sendBin  func([]byte) error
	sendText func(any) error

	mu  sync.Mutex
	cur *AudioSession
}

// pageAudioEnd tells the page its audio stream ended by itself.
type pageAudioEnd struct {
	Type  string `json:"type"` // "audio_end"
	Error string `json:"error,omitempty"`
}

// set starts or stops audio. It replaces a running stream.
func (a *audioBridge) set(on bool) (pageReply, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cur != nil {
		a.cur.Close()
		a.cur = nil
	}
	if !on {
		return pageReply{}, nil
	}
	ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
	defer cancel()
	as, err := OpenAudio(ctx, a.v.conn, AudioOptions{})
	if err != nil {
		return pageReply{}, err
	}
	a.cur = as
	go a.pump(as)
	h := as.Hello()
	return pageReply{Audio: &h}, nil
}

func (a *audioBridge) pump(as *AudioSession) {
	for {
		p, err := as.Next(a.ctx)
		if err != nil {
			a.mu.Lock()
			current := a.cur == as
			if current {
				a.cur = nil
			}
			a.mu.Unlock()
			if current && a.ctx.Err() == nil {
				a.sendText(pageAudioEnd{Type: "audio_end", Error: err.Error()})
			}
			return
		}
		msg := make([]byte, audioPageHeaderLen+len(p.Data))
		msg[0] = frameAudio
		binary.BigEndian.PutUint32(msg[1:], p.Seq)
		binary.BigEndian.PutUint64(msg[5:], uint64(a.sess.HostToLocal(p.Capture).UnixMicro()))
		binary.BigEndian.PutUint64(msg[13:], uint64(p.Received.UnixMicro()))
		copy(msg[audioPageHeaderLen:], p.Data)
		if a.sendBin(msg) != nil {
			return
		}
	}
}

func (a *audioBridge) stop() { a.set(false) }

// ---- file upload ----

// maxUpload bounds one uploaded file.
const maxUpload = 8 << 30

// uploadResult is the answer to an upload request.
type uploadResult struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	Error string `json:"error,omitempty"`
}

func writeUpload(w http.ResponseWriter, code int, r uploadResult) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(r)
}

// uploadName turns the name a page sends into a safe file name for the
// host: one path component, without characters that Windows hosts refuse.
func uploadName(s string) (string, error) {
	if !utf8.ValidString(s) {
		return "", errors.New("the file name is not valid UTF-8")
	}
	s = strings.Map(func(r rune) rune {
		switch {
		case r < 0x20 || r == 0x7f || strings.ContainsRune(`<>:"|?*/\`, r):
			return '_'
		}
		return r
	}, s)
	s = strings.TrimRight(s, ". ")
	if s == "" || s == "." || s == ".." {
		return "", errors.New("the file has no usable name")
	}
	if len(s) > 255 {
		// Keep the extension when shortening.
		ext := ""
		if i := strings.LastIndexByte(s, '.'); i > len(s)-16 && i > 0 {
			ext = s[i:]
		}
		s = strings.ToValidUTF8(s[:255-len(ext)], "") + ext
	}
	if !rfs.ValidName(s) {
		return "", errors.New("the file name is not valid")
	}
	return s, nil
}

// fsClient returns the (shared) file-service session to the host.
func (v *viewer) fsClient(ctx context.Context) (*rfs.Client, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.fs != nil {
		select {
		case <-v.fs.Done():
			v.fs = nil
		default:
			return v.fs, nil
		}
	}
	c, err := rfs.NewClient(ctx, v.conn)
	if err != nil {
		return nil, err
	}
	v.fs = c
	return c, nil
}

// handleUpload receives one file from the page (the request body) and
// copies it to the host with the file service. The page drives it with
// XMLHttpRequest, so its upload progress is the host's pace: the body is
// read only as fast as the host takes it.
func (v *viewer) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeUpload(w, http.StatusMethodNotAllowed, uploadResult{Error: "method not allowed"})
		return
	}
	// The request must come from the viewer page itself: a page of another
	// origin could otherwise write files to the remote machine through the
	// user's browser. The custom header cannot be set cross-origin without a
	// preflight, which this server does not answer.
	if !v.allowedOrigin(r.Header.Get("Origin")) || r.Header.Get("X-RemoHop-Upload") != "1" {
		writeUpload(w, http.StatusForbidden, uploadResult{Error: "forbidden"})
		return
	}
	if v.opts.ViewOnly {
		writeUpload(w, http.StatusForbidden, uploadResult{Error: "this is a view-only session: files cannot be uploaded"})
		return
	}
	name, err := uploadName(r.URL.Query().Get("name"))
	if err != nil {
		writeUpload(w, http.StatusBadRequest, uploadResult{Error: err.Error()})
		return
	}
	ctx := r.Context()
	fail := func(code int, err error) {
		writeUpload(w, code, uploadResult{Name: name, Error: err.Error()})
	}
	c, err := v.fsClient(ctx)
	if err != nil {
		fail(http.StatusBadGateway, fmt.Errorf("cannot reach the host's file service: %w", err))
		return
	}
	dir := v.uploadDir
	if dir == "" {
		if dir, err = c.DownloadsDir(ctx); err != nil {
			fail(http.StatusBadGateway, fmt.Errorf("cannot find the host's Downloads folder: %w", err))
			return
		}
	}
	dest := strings.TrimRight(dir, "/\\") + "/" + name
	if dir == "/" {
		dest = "/" + name
	}
	body := io.Reader(http.MaxBytesReader(w, r.Body, maxUpload))
	info, err := c.Upload(ctx, dest, body, rfs.PutOptions{Unique: true})
	if err != nil {
		fail(http.StatusBadGateway, err)
		return
	}
	writeUpload(w, http.StatusOK, uploadResult{Name: info.Name(), Path: strings.TrimRight(dir, "/\\") + "/" + info.Name(), Size: info.Size()})
}
