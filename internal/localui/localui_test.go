package localui

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func start(t *testing.T) *Server {
	t.Helper()
	s, err := New(fstest.MapFS{"index.html": {Data: []byte("<!doctype html>hi")}, "app.js": {Data: []byte("x")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.Hello(func() map[string]any { return map[string]any{"v": 1} })
	s.Post("/api/do", true, func(r *http.Request, b json.RawMessage) (any, error) {
		var x struct{ Fail bool }
		json.Unmarshal(b, &x)
		if x.Fail {
			return nil, Errorf("nope", "refused")
		}
		return map[string]any{"done": true}, nil
	})
	ch := make(chan struct{}, 1)
	s.Events("/api/events", func() any { return map[string]int{"n": 1} }, func() (<-chan struct{}, func()) { return ch, func() {} })
	go s.Serve()
	t.Cleanup(s.Close)
	return s
}

func do(t *testing.T, method, url, body string, hdr map[string]string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	for k, v := range hdr {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestSecurity(t *testing.T) {
	s := start(t)
	base := s.URL()
	root := strings.TrimSuffix(base, strings.SplitN(strings.TrimPrefix(base, "http://"), "/", 2)[1])
	if code, body := do(t, "GET", base, "", nil); code != 200 || !strings.Contains(body, "hi") {
		t.Fatalf("page: %d %q", code, body)
	}
	if code, _ := do(t, "GET", base+"assets/app.js", "", nil); code != 200 {
		t.Fatalf("asset: %d", code)
	}
	if code, _ := do(t, "GET", root+"wrongtoken/", "", nil); code != 404 {
		t.Fatalf("wrong token: %d", code)
	}
	if code, _ := do(t, "GET", base, "", map[string]string{"Host": "evil.example:" + s.port}); code != 403 {
		t.Fatalf("rebinding Host: %d", code)
	}
	json := map[string]string{"Content-Type": "application/json"}
	code, body := do(t, "POST", base+"api/hello", "{}", json)
	if code != 200 || !strings.Contains(body, s.CSRF()) || !strings.Contains(body, `"v":1`) {
		t.Fatalf("hello: %d %s", code, body)
	}
	if code, _ := do(t, "POST", base+"api/do", "{}", json); code != 403 {
		t.Fatalf("no CSRF: %d", code)
	}
	ok := map[string]string{"Content-Type": "application/json", HeaderCSRF: s.CSRF()}
	if code, body := do(t, "POST", base+"api/do", "{}", ok); code != 200 || !strings.Contains(body, "done") {
		t.Fatalf("with CSRF: %d %s", code, body)
	}
	if code, body := do(t, "POST", base+"api/do", `{"fail":true}`, ok); code != 409 || !strings.Contains(body, `"code":"nope"`) {
		t.Fatalf("coded error: %d %s", code, body)
	}
	cross := map[string]string{"Content-Type": "application/json", HeaderCSRF: s.CSRF(), "Origin": "http://evil.example"}
	if code, _ := do(t, "POST", base+"api/do", "{}", cross); code != 403 {
		t.Fatalf("cross-origin POST: %d", code)
	}
	site := map[string]string{"Sec-Fetch-Site": "cross-site"}
	if code, _ := do(t, "GET", base+"api/events", "", site); code != 403 {
		t.Fatalf("cross-site GET: %d", code)
	}
	if code, _ := do(t, "POST", base+"api/do", "{}", map[string]string{HeaderCSRF: s.CSRF(), "Content-Type": "text/plain"}); code != 415 {
		t.Fatalf("form POST: %d", code)
	}
	if code, _ := do(t, "POST", base+"api/do", "{bad", ok); code != 400 {
		t.Fatalf("bad JSON: %d", code)
	}
}

func TestEventsAndPresence(t *testing.T) {
	s := start(t)
	if v, seen, _ := s.Presence(); v != 0 || seen {
		t.Fatal("presence before any page")
	}
	resp, err := http.Get(s.URL() + "api/events")
	if err != nil {
		t.Fatal(err)
	}
	line, _ := bufio.NewReader(resp.Body).ReadString('\n')
	if !strings.HasPrefix(line, `data: {"n":1}`) {
		t.Fatalf("event %q", line)
	}
	if v, seen, _ := s.Presence(); v != 1 || !seen {
		t.Fatalf("presence with a page: %d %v", v, seen)
	}
	resp.Body.Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		v, _, gone := s.Presence()
		if v == 0 && !gone.IsZero() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the closed stream still counts")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
