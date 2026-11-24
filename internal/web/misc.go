package web

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"unicode/utf8"

	"github.com/readori/RemoHop/internal/svc/clip"
	"github.com/readori/RemoHop/internal/svc/screenshot"
	"github.com/readori/RemoHop/internal/svc/sysinfo"
)

const maxClipBody = 8 << 20

// handleClipGet fetches the remote clipboard on an explicit button press.
// Text is returned; an image is reported by size only.
func (s *Server) handleClipGet(w http.ResponseWriter, r *http.Request, ws *webSession) {
	var req struct {
		C string `json:"c"`
	}
	if aerr := readJSON(w, r, &req, 1024); aerr != nil {
		writeErr(w, aerr)
		return
	}
	p, release, ok := s.conn(w, ws, req.C)
	if !ok {
		return
	}
	defer release()
	c, err := clip.GetWith(r.Context(), p.current(), clip.CallOptions{NoHistory: true})
	switch {
	case errors.Is(err, clip.ErrEmpty):
		writeJSON(w, http.StatusOK, map[string]any{"kind": "empty"})
	case err != nil:
		writeErr(w, classify(err))
	case c.Kind == clip.KindText:
		writeJSON(w, http.StatusOK, map[string]any{"kind": "text", "text": c.String()})
	default:
		// The page does not render remote images from the clipboard;
		// the Info tab's screenshot is the way to look at a screen.
		writeJSON(w, http.StatusOK, map[string]any{"kind": "image", "size": len(c.Data), "png": base64.StdEncoding.EncodeToString(c.Data)})
	}
}

func (s *Server) handleClipSet(w http.ResponseWriter, r *http.Request, ws *webSession) {
	var req struct {
		C    string `json:"c"`
		Text string `json:"text"`
	}
	if aerr := readJSON(w, r, &req, maxClipBody); aerr != nil {
		writeErr(w, aerr)
		return
	}
	if req.Text == "" || !utf8.ValidString(req.Text) {
		writeErr(w, &apiError{Status: http.StatusBadRequest, Code: "bad_request", Message: "nothing to send"})
		return
	}
	p, release, ok := s.conn(w, ws, req.C)
	if !ok {
		return
	}
	defer release()
	if err := clip.SetWith(r.Context(), p.current(), clip.Text(req.Text), clip.CallOptions{NoHistory: true}); err != nil {
		writeErr(w, classify(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request, ws *webSession) {
	p, release, ok := s.conn(w, ws, r.URL.Query().Get("c"))
	if !ok {
		return
	}
	defer release()
	info, err := sysinfo.Get(r.Context(), p.current())
	if err != nil {
		writeErr(w, classify(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"info": info, "conn": p.snapshot()})
}

func (s *Server) handleScreenshot(w http.ResponseWriter, r *http.Request, ws *webSession) {
	p, release, ok := s.conn(w, ws, r.URL.Query().Get("c"))
	if !ok {
		return
	}
	defer release()
	display, _ := strconv.Atoi(r.URL.Query().Get("display"))
	img, err := screenshot.Capture(r.Context(), p.current(), screenshot.Options{Display: display, Format: screenshot.PNG})
	if err != nil {
		writeErr(w, classify(err))
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Write(img.Data)
}
