package mobile

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/dial"
	"github.com/readori/RemoHop/internal/nat"
	"github.com/readori/RemoHop/internal/signal"
)

func codesInit(t *testing.T) string {
	t.Helper()
	StopGateway()
	t.Setenv("REMOHOP_SIGNAL", "")
	d := filepath.Join(t.TempDir(), "files", "remohop")
	if err := Init(d, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(StopGateway)
	return d
}

// outcome reads a JSON outcome; code is the reason of a bad_request.
func outcome(t *testing.T, js string) (ok bool, code, msg string) {
	t.Helper()
	var r struct {
		OK     bool   `json:"ok"`
		Code   string `json:"code"`
		Reason string `json:"reason"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal([]byte(js), &r); err != nil {
		t.Fatalf("%s: %v", js, err)
	}
	if r.Code == "bad_request" {
		if r.Reason == "" {
			t.Fatalf("%s: bad_request without a reason", js)
		}
		return r.OK, r.Reason, r.Error
	}
	return r.OK, r.Code, r.Error
}

// TestPairErrorCodes: every failure a user can cause from the Pair screen
// comes with a code the apps translate (the text is the core's English,
// which names CLI commands, and is for the diagnostic log only).
func TestPairErrorCodes(t *testing.T) {
	codesInit(t)
	cases := []struct{ target, pw, alias, code string }{
		{"", "pw", "", "target_required"},
		{"123456789", "", "", "password_required"},
		{"myhost", "pw", "", "bad_target"},
		{"123456789", "pw", "1 bad", "bad_alias"},
		{"123 456 789", "pw", "", "no_rendezvous"},
	}
	for _, c := range cases {
		js, err := PairWithPassword(c.target, c.pw, c.alias)
		if err != nil {
			t.Fatal(err)
		}
		ok, code, msg := outcome(t, js)
		if ok || code != c.code {
			t.Errorf("PairWithPassword(%q, _, %q) = %s, want code %s", c.target, c.alias, js, c.code)
		}
		if msg == "" {
			t.Errorf("%s: no text for the diagnostic log", c.code)
		}
	}
}

func TestPairWrongPasswordCode(t *testing.T) {
	codesInit(t)
	addr := startHost(t)
	js, err := PairWithPassword(addr, "not-the-password", "")
	if err != nil {
		t.Fatal(err)
	}
	if ok, code, _ := outcome(t, js); ok || code != "bad_password" {
		t.Fatalf("got %s, want bad_password", js)
	}
}

func TestCodeOf(t *testing.T) {
	cases := []struct {
		err  error
		code string
	}{
		{fmt.Errorf("x: %w", signal.ErrNotConfigured), "no_rendezvous"},
		{fmt.Errorf("no remohop host answered at 127.0.0.1:1: %w", context.DeadlineExceeded), "host_unreachable"},
		{fmt.Errorf("rendezvous server example.com:21116 unreachable: %w", context.DeadlineExceeded), "rendezvous_unreachable"},
		{fmt.Errorf("cannot resolve x.invalid:1: %w", errors.New("lookup failed")), "cannot_resolve"},
		{fmt.Errorf("dial: %w", nat.ErrNoPath), "no_path"},
		{fmt.Errorf("dial: %w", dial.ErrNoCredential), "password_required"},
		{fmt.Errorf("dial: %w", dial.ErrHostKeyChanged), "host_key_changed"},
		{context.DeadlineExceeded, "timeout"},
		{context.Canceled, "canceled"},
		{errors.New("something else"), "error"},
	}
	for _, c := range cases {
		if got := codeOf(c.err); got != c.code {
			t.Errorf("codeOf(%v) = %s, want %s", c.err, got, c.code)
		}
	}
}

func TestSetSettingJSON(t *testing.T) {
	codesInit(t)
	cases := []struct{ key, value, code string }{
		{"signal", "rv.example.com:21116", ""},
		{"signal", "foo:99999", "bad_address"},
		{"signal", "a b", "bad_address"},
		{"signal-pin", "not-a-key", "bad_key"},
		{"name", strings.Repeat("x", 65), "bad_name"},
		{"name", "Phone", ""},
		{"relay", "maybe", "bad_value"},
		{"outcomes", "maybe", "bad_value"},
		{"outcomes", "off", ""},
		{"password", "x", "error"},
	}
	for _, c := range cases {
		js, err := SetSettingJSON(c.key, c.value)
		if err != nil {
			t.Fatal(err)
		}
		ok, code, _ := outcome(t, js)
		if c.code == "" && !ok || c.code != "" && (ok || code != c.code) {
			t.Errorf("SetSettingJSON(%q, %q) = %s, want %q", c.key, c.value, js, c.code)
		}
	}
	if v, _ := GetSetting("signal"); v != "rv.example.com:21116" {
		t.Errorf("a refused value replaced the saved one: %q", v)
	}
}

func TestPendingConnectionsWithoutGateway(t *testing.T) {
	codesInit(t)
	if n := PendingConnections(); n != 0 {
		t.Fatalf("PendingConnections() = %d with no gateway", n)
	}
}

// TestPendingConnections: a connection attempt that has not finished
// (here: an address where nothing answers) counts, so the apps keep the
// process alive while the user waits for approval; it stops counting once
// cancelled.
func TestPendingConnections(t *testing.T) {
	codesInit(t)
	base, err := StartGateway()
	if err != nil {
		t.Fatal(err)
	}
	var sid, csrf string
	call := func(method, path string, body, out any) {
		t.Helper()
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, base+"api/v1/"+path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		if sid != "" {
			req.Header.Set("X-RemoHop-Session", sid)
			req.Header.Set("X-RemoHop-CSRF", csrf)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		rb, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 300 {
			t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, rb)
		}
		if out != nil {
			if err := json.Unmarshal(rb, out); err != nil {
				t.Fatalf("%s: %v", rb, err)
			}
		}
	}
	var sess struct{ SID, CSRF string }
	call("POST", "session", map[string]string{"sid": ""}, &sess)
	sid, csrf = sess.SID, sess.CSRF
	var att struct{ Attempt, State string }
	call("POST", "connect", map[string]any{"target": "127.0.0.1:1", "password": "x"}, &att)
	if n := PendingConnections(); n != 1 {
		t.Fatalf("PendingConnections() = %d during an attempt (%+v)", n, att)
	}
	call("DELETE", "connect/"+att.Attempt, nil, nil)
	deadline := time.Now().Add(5 * time.Second)
	for PendingConnections() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("a cancelled attempt still counts")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
