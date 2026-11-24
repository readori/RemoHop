package unattended

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/svc/screenshot"
)

func TestHandshake(t *testing.T) {
	secret := bytes.Repeat([]byte{7}, 32)
	run := func(serverSecret, clientSecret []byte) (serr, cerr error) {
		a, b := net.Pipe()
		defer a.Close()
		defer b.Close()
		a.SetDeadline(time.Now().Add(5 * time.Second))
		b.SetDeadline(time.Now().Add(5 * time.Second))
		done := make(chan error, 1)
		go func() {
			err := serverHandshake(a, serverSecret)
			if err != nil {
				a.Close() // a real helper hangs up on a failed handshake
			}
			done <- err
		}()
		cerr = clientHandshake(b, clientSecret)
		if cerr != nil {
			b.Close()
		}
		return <-done, cerr
	}
	if serr, cerr := run(secret, secret); serr != nil || cerr != nil {
		t.Fatalf("same secret: server %v, client %v", serr, cerr)
	}
	other := bytes.Repeat([]byte{8}, 32)
	serr, cerr := run(secret, other)
	if !errors.Is(serr, errAuth) {
		t.Errorf("server accepted a client with the wrong secret: %v", serr)
	}
	if cerr == nil {
		t.Error("client thinks it is authenticated although the server refused it")
	}
	// A server that does not know the secret cannot fake its answer.
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	go func() {
		ns := make([]byte, nonceLen)
		a.Write(ns)
		in := make([]byte, nonceLen+macLen)
		io.ReadFull(a, in)
		a.Write(make([]byte, macLen)) // a guess
	}()
	if err := clientHandshake(b, secret); !errors.Is(err, errAuth) {
		t.Errorf("client accepted an impostor server: %v", err)
	}
	// MACs are bound to direction: the client's MAC replayed as the
	// server's does not verify.
	ns, nc := bytes.Repeat([]byte{1}, nonceLen), bytes.Repeat([]byte{2}, nonceLen)
	if bytes.Equal(handshakeMAC(secret, labelSvc, ns, nc), handshakeMAC(secret, labelHelpr, nc, ns)) {
		t.Error("service and helper MACs coincide")
	}
}

func TestMessages(t *testing.T) {
	var b bytes.Buffer
	if err := writeJSON(&b, msgRequest, request{Op: opMetric, Index: 78}); err != nil {
		t.Fatal(err)
	}
	if err := writeMsg(&b, msgData, []byte("pixels")); err != nil {
		t.Fatal(err)
	}
	var req request
	if err := readJSON(&b, msgRequest, &req); err != nil || req.Op != opMetric || req.Index != 78 {
		t.Fatalf("request: %+v, %v", req, err)
	}
	typ, data, err := readMsg(&b, nil)
	if err != nil || typ != msgData || string(data) != "pixels" {
		t.Fatalf("data: %q %q %v", typ, data, err)
	}
	if _, _, err := readMsg(&b, nil); err != io.EOF {
		t.Fatalf("at the end: %v", err)
	}
	// Oversized JSON is refused before reading it.
	big := []byte{msgRequest, 0x10, 0, 0, 0}
	if _, _, err := readMsg(bytes.NewReader(big), nil); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("oversized JSON: %v", err)
	}
	// Truncated payload.
	if _, _, err := readMsg(bytes.NewReader([]byte{msgData, 0, 0, 0, 9, 1, 2}), nil); err != io.ErrUnexpectedEOF {
		t.Errorf("truncated: %v", err)
	}
	// Wrong type.
	b.Reset()
	writeJSON(&b, msgReply, reply{})
	if err := readJSON(&b, msgRequest, &req); err == nil {
		t.Error("readJSON accepted the wrong message type")
	}
}

func TestHelperConfig(t *testing.T) {
	c, err := newHelperConfig(1234, 2)
	if err != nil {
		t.Fatal(err)
	}
	line, _ := json.Marshal(c)
	got, secret, err := parseHelperConfig(append(line, '\n'))
	if err != nil || got != c || len(secret) != 32 {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	c2, _ := newHelperConfig(1234, 2)
	if c2.Pipe == c.Pipe || c2.Secret == c.Secret {
		t.Error("two launches got the same pipe name or secret")
	}
	bad := []helperConfig{
		{Pipe: `\\.\pipe\other`, Secret: c.Secret, Parent: 1},
		{Pipe: c.Pipe + "x", Secret: c.Secret, Parent: 1},
		{Pipe: `\\server\pipe\remohop-ua-` + strings.Repeat("a", 32), Secret: c.Secret, Parent: 1},
		{Pipe: c.Pipe, Secret: "abcd", Parent: 1},
		{Pipe: c.Pipe, Secret: strings.Repeat("zz", 32), Parent: 1},
		{Pipe: c.Pipe, Secret: c.Secret, Parent: 0},
	}
	for _, b := range bad {
		line, _ := json.Marshal(b)
		if _, _, err := parseHelperConfig(line); err == nil {
			t.Errorf("accepted %+v", b)
		}
	}
	if _, _, err := parseHelperConfig([]byte("not json")); err == nil {
		t.Error("accepted garbage")
	}
}

func TestPickSession(t *testing.T) {
	sessions := []sessionInfo{
		{ID: 0, State: wtsDisconnected, Name: "Services"},
		{ID: 1, State: wtsConnected, Name: "Console"}, // sign-in screen
		{ID: 3, State: wtsActive, Name: "RDP-Tcp#0"},
		{ID: 2, State: wtsActive, Name: "RDP-Tcp#1"},
	}
	cases := []struct {
		mode    string
		console uint32
		list    []sessionInfo
		want    uint32
		ok      bool
	}{
		{"", 1, sessions, 1, true},                  // console: the sign-in screen
		{"console", 1, sessions, 1, true},           // same
		{"active", 1, sessions, 2, true},            // the lowest active remote session
		{"ACTIVE", 1, sessions, 2, true},            // case-insensitive
		{"", noSession, sessions, noSession, false}, // console between sessions
		{"active", noSession, sessions, 2, true},
		{"", 0, sessions, 0, false}, // never session 0
		{"active", 1, []sessionInfo{{ID: 1, State: wtsActive}, {ID: 2, State: wtsActive}}, 1, true}, // console has a user
		{"active", 1, []sessionInfo{{ID: 1, State: wtsConnected}}, 1, true},                         // nobody active: console
		{"active", noSession, nil, noSession, false},
	}
	for _, c := range cases {
		got, ok := pickSession(c.mode, c.console, c.list)
		if got != c.want || ok != c.ok {
			t.Errorf("pickSession(%q, %d) = %d, %v; want %d, %v", c.mode, c.console, got, ok, c.want, c.ok)
		}
	}
}

func TestRelaunchDelay(t *testing.T) {
	want := []time.Duration{0, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second, 30 * time.Second}
	for n, w := range want {
		if got := relaunchDelay(n); got != w {
			t.Errorf("relaunchDelay(%d) = %v, want %v", n, got, w)
		}
	}
	if relaunchDelay(1000) != 30*time.Second {
		t.Error("no upper bound")
	}
}

func TestSASAllowed(t *testing.T) {
	for v, want := range map[uint32]bool{0: false, 1: true, 2: false, 3: true, 4: false} {
		if got := sasAllowed(v, true); got != want {
			t.Errorf("sasAllowed(%d) = %v", v, got)
		}
	}
	if sasAllowed(1, false) {
		t.Error("an absent policy allows SAS")
	}
}

func TestDesktopKind(t *testing.T) {
	for name, want := range map[string]string{"Default": "user's desktop", "Winlogon": "secure desktop", "Screen-saver": "screen saver", "X": "desktop X"} {
		if got := DesktopKind(name); !strings.Contains(got, want) {
			t.Errorf("DesktopKind(%q) = %q", name, got)
		}
	}
}

func TestCopyFrames(t *testing.T) {
	const size = 8
	f1, f2 := bytes.Repeat([]byte{1}, size), bytes.Repeat([]byte{2}, size)
	var src bytes.Buffer
	writeMsg(&src, msgFrame, f1)
	writeMsg(&src, msgSame, nil)
	writeMsg(&src, msgFrame, f2)
	writeMsg(&src, msgSame, nil)
	writeMsg(&src, msgSame, nil)
	writeJSON(&src, msgEnd, reply{})
	var dst bytes.Buffer
	if err := copyFrames(&dst, &src, size); err != nil {
		t.Fatal(err)
	}
	want := bytes.Join([][]byte{f1, f1, f2, f2, f2}, nil)
	if !bytes.Equal(dst.Bytes(), want) {
		t.Fatalf("got %v\nwant %v", dst.Bytes(), want)
	}

	// The end carries the helper's reason.
	src.Reset()
	writeMsg(&src, msgFrame, f1)
	writeJSON(&src, msgEnd, reply{Error: "the display changed"})
	if err := copyFrames(io.Discard, &src, size); err == nil || err.Error() != "the display changed" {
		t.Errorf("end with reason: %v", err)
	}
	// Bad streams.
	for name, msgs := range map[string]func(w io.Writer){
		"same first":  func(w io.Writer) { writeMsg(w, msgSame, nil) },
		"wrong size":  func(w io.Writer) { writeMsg(w, msgFrame, f1[:3]) },
		"unknown msg": func(w io.Writer) { writeMsg(w, msgReply, nil) },
	} {
		src.Reset()
		msgs(&src)
		if err := copyFrames(io.Discard, &src, size); err == nil || err == io.EOF {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A pipe that breaks mid-stream is an error, not a normal end.
	src.Reset()
	writeMsg(&src, msgFrame, f1)
	if err := copyFrames(io.Discard, &src, size); err != io.EOF {
		t.Errorf("broken stream: %v", err)
	}
}

func TestDisplayArea(t *testing.T) {
	ds := []screenshot.Display{
		{Index: 0, X: 0, Y: 0, Width: 1920, Height: 1080, Primary: true},
		{Index: 1, X: -1280, Y: 100, Width: 1280, Height: 1024},
	}
	r, err := displayArea(ds, 0)
	if err != nil || r != (rect{0, 0, 1920, 1080}) {
		t.Errorf("display 0: %+v %v", r, err)
	}
	r, err = displayArea(ds, 1)
	if err != nil || r != (rect{-1280, 100, 1280, 1024}) {
		t.Errorf("display 1: %+v %v", r, err)
	}
	all, err := displayArea(ds, screenshot.AllDisplays)
	if err != nil || all != (rect{-1280, 0, 3200, 1124}) {
		t.Errorf("all: %+v %v", all, err)
	}
	if _, err := displayArea(ds, 2); err == nil {
		t.Error("display 2 exists?")
	}
	if _, err := displayArea(nil, 0); err == nil {
		t.Error("no displays, no error")
	}
	for _, r := range []rect{{0, 0, 1920, 1080}, {-1280, 100, 1280, 1024}, all} {
		if !rectMatches(r, ds) {
			t.Errorf("%+v does not match", r)
		}
	}
	// After a resolution change the old area no longer matches.
	changed := []screenshot.Display{{X: 0, Y: 0, Width: 1280, Height: 720, Primary: true}}
	if rectMatches(rect{0, 0, 1920, 1080}, changed) {
		t.Error("an old area matches a changed layout")
	}
	if rectMatches(rect{0, 0, 1, 1}, nil) {
		t.Error("matches no displays")
	}
}

func TestSASPolicyHintFits(t *testing.T) {
	if len(SASPolicyHint) > 300 {
		t.Errorf("the hint is %d characters; viewers cut reasons at 300", len(SASPolicyHint))
	}
}
