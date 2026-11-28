package desktop

import (
	"embed"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"runtime"
	"strings"
	"sync"

	"github.com/readori/RemoHop/internal/localui"
)

//go:embed assets
var assetFS embed.FS

// UI is what the page may ask of the window around it.
type UI interface {
	// Show brings the main window up.
	Show()
	// Hide puts it away (into the tray).
	Hide()
	// PopOut shows url (a host's console) in a window of its own.
	PopOut(url, title string) error
	// Quit ends the app.
	Quit()
	// Info describes the shell.
	Info() UIInfo
}

// UIInfo describes the window shell to the page.
type UIInfo struct {
	// Native: a native window (WebView2, WKWebView, WebKitGTK); otherwise
	// a browser window.
	Native bool   `json:"native"`
	Engine string `json:"engine,omitempty"`
	// Tray: closing the window keeps the app in the tray.
	Tray     bool   `json:"tray"`
	Platform string `json:"platform"`
}

// PageState is State plus what the page needs about its surroundings.
type PageState struct {
	State
	UI         UIInfo          `json:"ui"`
	Autostart  AutostartState  `json:"autostart"`
	Unattended UnattendedState `json:"unattended"`
	// Smoke is the screen the smoke test wants shown (see smoke.go).
	Smoke string `json:"smoke,omitempty"`
}

// Server is the app's UI server: the page, its API and its event stream.
type Server struct {
	*localui.Server
	app *App
	log *slog.Logger

	mu        sync.Mutex
	ui        UI
	smokeView string
}

// validSID accepts a gateway session id (base64url).
func validSID(s string) bool {
	if len(s) < 16 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// NewServer prepares the UI server for app on a free loopback port.
func NewServer(app *App, log *slog.Logger) (*Server, error) {
	sub, _ := fs.Sub(assetFS, "assets")
	ls, err := localui.New(sub, log)
	if err != nil {
		return nil, err
	}
	s := &Server{Server: ls, app: app, log: log, ui: noUI{}}
	app.onGateway = ls.SetFrameSrc
	s.routes()
	return s, nil
}

// Origin is the page's origin (http://127.0.0.1:PORT).
func (s *Server) Origin() string {
	u := s.URL()
	return u[:strings.Index(u[len("http://"):], "/")+len("http://")]
}

// SetUI connects the window shell.
func (s *Server) SetUI(ui UI) {
	s.mu.Lock()
	s.ui = ui
	s.mu.Unlock()
}

func (s *Server) shell() UI {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ui
}

// PageState is what the page shows.
func (s *Server) PageState() PageState {
	return PageState{State: s.app.State(), UI: s.shell().Info(), Autostart: autostartState(), Unattended: s.app.UnattendedState(), Smoke: s.smoke()}
}

func decode(b json.RawMessage, v any) error {
	if len(b) == 0 {
		return nil
	}
	if err := json.Unmarshal(b, v); err != nil {
		return Errorf("bad_request", "bad request")
	}
	return nil
}

func (s *Server) routes() {
	a := s.app
	s.Hello(func() map[string]any { return map[string]any{"state": s.PageState()} })
	s.Events("/api/events", func() any { return s.PageState() }, a.Subscribe)
	type req struct {
		N        int    `json:"n"`
		ID       int    `json:"id"`
		On       bool   `json:"on"`
		Allow    bool   `json:"allow"`
		Control  bool   `json:"control"`
		Files    bool   `json:"files"`
		Remember bool   `json:"remember"`
		Mode     string `json:"mode"`
		Addr     string `json:"addr"`
		Lang     string `json:"lang"`
		Theme    string `json:"theme"`
		Target   string `json:"target"`
		Alias    string `json:"alias"`
		MAC      string `json:"mac"`
		Identity string `json:"identity"`
		Title    string `json:"title"`
		Password string `json:"password"`
		Refresh  bool   `json:"refresh"`
		SID      string `json:"sid"`
		Version  string `json:"version"`
	}
	post := func(path string, h func(r *http.Request, q req) (any, error)) {
		s.Post(path, true, func(r *http.Request, b json.RawMessage) (any, error) {
			var q req
			if err := decode(b, &q); err != nil {
				return nil, err
			}
			return h(r, q)
		})
	}
	post("/api/incoming", func(_ *http.Request, q req) (any, error) { return nil, a.SetIncoming(q.On) })
	post("/api/regenerate", func(*http.Request, req) (any, error) { return nil, a.Regenerate() })
	post("/api/own-password", func(_ *http.Request, q req) (any, error) { return nil, a.SetOwnPassword(q.Password) })
	post("/api/decide", func(_ *http.Request, q req) (any, error) {
		return nil, a.Decide(q.N, q.Allow, q.Control, q.Files, q.Remember)
	})
	post("/api/session", func(_ *http.Request, q req) (any, error) { return nil, a.SetSessionAccess(q.ID, q.Control, q.Files) })
	post("/api/disconnect", func(_ *http.Request, q req) (any, error) { return nil, a.Disconnect(q.ID) })
	post("/api/edit-device", func(_ *http.Request, q req) (any, error) {
		return nil, a.EditDevice(q.Identity, q.Alias, q.Control, q.Files)
	})
	post("/api/open-page", func(_ *http.Request, q req) (any, error) {
		u, err := sitePage(q.Target, q.Lang)
		if err != nil {
			return nil, err
		}
		return nil, openURL(u)
	})
	post("/api/forget-device", func(_ *http.Request, q req) (any, error) { return nil, a.ForgetDevice(q.Identity) })
	post("/api/server", func(_ *http.Request, q req) (any, error) { return nil, a.SetServer(q.Mode, q.Addr) })
	post("/api/setup", func(_ *http.Request, q req) (any, error) { return nil, a.FinishSetup(q.Mode, q.Addr, q.Lang) })
	post("/api/lang", func(_ *http.Request, q req) (any, error) { return nil, a.SetLang(q.Lang) })
	post("/api/theme", func(_ *http.Request, q req) (any, error) { return nil, a.SetTheme(q.Theme) })
	post("/api/autostart", func(_ *http.Request, q req) (any, error) {
		err := setAutostart(q.On)
		a.wake()
		return nil, err
	})
	post("/api/peers", func(r *http.Request, q req) (any, error) {
		peers, errText := a.Peers(r.Context(), q.Refresh)
		return map[string]any{"peers": peers, "error": errText}, nil
	})
	post("/api/lan", func(r *http.Request, _ req) (any, error) {
		hosts, err := a.LAN(r.Context())
		errText := ""
		if err != nil {
			errText = err.Error()
		}
		return map[string]any{"hosts": hosts, "error": errText}, nil
	})
	post("/api/peer/alias", func(_ *http.Request, q req) (any, error) { return nil, a.SetAlias(q.Target, q.Alias) })
	post("/api/peer/forget", func(_ *http.Request, q req) (any, error) { return nil, a.Forget(q.Target) })
	post("/api/peer/wake", func(r *http.Request, q req) (any, error) { return nil, a.Wake(r.Context(), q.Target) })
	post("/api/peer/password", func(_ *http.Request, q req) (any, error) {
		pw, err := a.SavedPassword(q.Target, false)
		if err != nil {
			return nil, err
		}
		return map[string]any{"password": pw}, nil
	})
	post("/api/peer/password/forget", func(_ *http.Request, q req) (any, error) {
		_, err := a.SavedPassword(q.Target, true)
		return nil, err
	})
	post("/api/peer/mac", func(_ *http.Request, q req) (any, error) { return nil, a.SetWake(q.Target, q.MAC) })
	post("/api/console", func(_ *http.Request, q req) (any, error) {
		u, err := a.ConsoleURL(q.Target)
		if err != nil {
			return nil, err
		}
		return map[string]any{"url": u}, nil
	})
	post("/api/console/close", func(_ *http.Request, q req) (any, error) {
		a.CloseConsole(q.Target)
		return nil, nil
	})
	post("/api/popout", func(_ *http.Request, q req) (any, error) {
		u, err := a.ConsoleURL(q.Target)
		if err != nil {
			return nil, err
		}
		// The window joins the page's gateway session (the console handed
		// its id over), so the host is not connected to again. The id
		// goes in the fragment, which never reaches a server.
		if q.SID != "" {
			if !validSID(q.SID) {
				return nil, Errorf("bad_request", "bad session")
			}
			u += "#sid=" + q.SID
		}
		title := strings.TrimSpace(q.Title)
		if title == "" {
			title = q.Target
		}
		return nil, s.shell().PopOut(u, "RemoHop · "+clean(title))
	})
	post("/api/unattended/enable", func(r *http.Request, q req) (any, error) {
		return nil, a.EnableUnattended(r.Context(), q.Password)
	})
	post("/api/unattended/disable", func(r *http.Request, q req) (any, error) { return nil, a.DisableUnattended(r.Context()) })
	post("/api/unattended/sessions", func(r *http.Request, q req) (any, error) {
		ss, err := a.ServiceSessions(r.Context())
		if err != nil {
			return nil, err
		}
		return map[string]any{"sessions": ss}, nil
	})
	post("/api/unattended/disconnect", func(r *http.Request, q req) (any, error) { return nil, a.ServiceDisconnect(r.Context(), q.ID) })
	post("/api/unattended/password", func(r *http.Request, q req) (any, error) { return nil, a.ServicePassword(r.Context(), q.Password) })
	post("/api/unattended/reveal", func(r *http.Request, q req) (any, error) {
		pw, err := a.UnattendedReveal(r.Context())
		if err != nil {
			return nil, err
		}
		return map[string]string{"password": pw}, nil
	})
	post("/api/diagnostics/export", func(*http.Request, req) (any, error) {
		path, err := a.ExportDiagnostics(s.shell().Info())
		if err != nil {
			return nil, err
		}
		return map[string]any{"path": path}, nil
	})
	post("/api/update/check", func(r *http.Request, _ req) (any, error) {
		a.CheckUpdate(r.Context())
		return nil, nil
	})
	post("/api/update/auto", func(_ *http.Request, q req) (any, error) { return nil, a.SetUpdateAuto(q.On) })
	post("/api/update/skip", func(_ *http.Request, q req) (any, error) { return nil, a.SkipUpdate(q.Version) })
	post("/api/update/install", func(_ *http.Request, _ req) (any, error) {
		return nil, a.InstallUpdate(func() { s.shell().Quit() })
	})
	post("/api/update/open", func(_ *http.Request, q req) (any, error) {
		u, err := a.UpdateLink(q.Target, q.Lang)
		if err != nil {
			return nil, err
		}
		return nil, openURL(u)
	})
	post("/api/show", func(*http.Request, req) (any, error) { s.shell().Show(); return nil, nil })
	post("/api/hide", func(*http.Request, req) (any, error) { s.shell().Hide(); return nil, nil })
	post("/api/quit", func(*http.Request, req) (any, error) {
		go s.shell().Quit()
		return nil, nil
	})
	s.Post("/api/", false, func(*http.Request, json.RawMessage) (any, error) {
		return nil, &localui.Error{Status: http.StatusNotFound, Code: "not_found", Message: "no such API call"}
	})
}

// noUI is the shell before one is connected (and in tests).
type noUI struct{}

func (noUI) Show() {}
func (noUI) Hide() {}
func (noUI) PopOut(string, string) error {
	return Errorf("no_window", "no window can be opened here")
}
func (noUI) Quit()        {}
func (noUI) Info() UIInfo { return UIInfo{Platform: runtime.GOOS} }
