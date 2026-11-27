package signal

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
)

var testRelayPolicy struct {
	mu     sync.Mutex
	member ed25519.PublicKey
	ended  []endedRelay
}

type endedRelay struct {
	client, host ed25519.PublicKey
	bytes        int64
}

func init() {
	RegisterRequest("x-test-echo", func(_ context.Context, s *Server, r ExtRequest) (any, error) {
		return map[string]any{"key": []byte(r.Key), "payload": r.Payload, "source": r.Source.String(), "server": s != nil && s.DataDir() != "" && s.Logger() != nil}, nil
	})
	RegisterRequest("x-test-fail", func(context.Context, *Server, ExtRequest) (any, error) {
		return nil, &ExtError{Code: CodeRateLimited, Msg: "slow down", RetryAfter: 3 * time.Second}
	})
	RegisterRequest("x-test-big", func(_ context.Context, _ *Server, r ExtRequest) (any, error) {
		var n int
		json.Unmarshal(r.Payload, &n)
		return strings.Repeat("x", n), nil
	})
	RegisterRelayPolicy(RelayPolicy{Name: "test",
		Member: func(key ed25519.PublicKey) bool {
			testRelayPolicy.mu.Lock()
			defer testRelayPolicy.mu.Unlock()
			return testRelayPolicy.member != nil && bytes.Equal(key, testRelayPolicy.member)
		},
		Ended: func(client, host ed25519.PublicKey, n int64, _ time.Duration) {
			testRelayPolicy.mu.Lock()
			defer testRelayPolicy.mu.Unlock()
			testRelayPolicy.ended = append(testRelayPolicy.ended, endedRelay{client, host, n})
		}})
}

func TestExtRequests(t *testing.T) {
	as := startAccounts(t, t.TempDir(), generous(AccountsConfig{}))
	ctx := tctx(t)
	c := as.client()
	id := newID(t)

	var echo struct {
		Key     []byte          `json:"key"`
		Payload json.RawMessage `json:"payload"`
		Source  string          `json:"source"`
		Server  bool            `json:"server"`
	}
	raw, err := c.Ext(ctx, "x-test-echo", nil, map[string]int{"a": 1})
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(raw, &echo) != nil || echo.Key != nil || string(echo.Payload) != `{"a":1}` || echo.Source != "127.0.0.1" || !echo.Server {
		t.Fatalf("anonymous echo: %s", raw)
	}
	raw, err = c.Ext(ctx, "x-test-echo", id, "hi")
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(raw, &echo) != nil || !bytes.Equal(echo.Key, id.Public()) {
		t.Fatalf("proven echo: %s", raw)
	}

	// A key without a valid proof for this connection is refused.
	other := as.client()
	cs := other.q.ConnectionState().TLS
	exp, _ := cs.ExportKeyingMaterial(extExporterLabel, nil, 32)
	for _, proof := range [][]byte{nil, make([]byte, 64), id.Sign(extProofMessage("x-test-echo", exp)), id.Sign(extProofMessage("x-test-big", nil))} {
		err := c.roundTrip(ctx, func(st *quic.Stream) error {
			writeMsg(st, request{T: "x-test-echo", V: protoVersion, Key: id.Public(), Proof: proof})
			writeMsg(st, extBody{Ext: json.RawMessage(`1`)})
			var resp response
			if err := readMsg(st, &resp); err != nil {
				return err
			}
			return resp.err()
		})
		if CodeOf(err) != CodeBadRequest {
			t.Fatalf("bad proof accepted: %v", err)
		}
	}

	// Errors keep their code; unknown types are refused.
	if _, err := c.Ext(ctx, "x-test-fail", nil, nil); CodeOf(err) != CodeRateLimited {
		t.Fatalf("handler error: %v", err)
	}
	if _, err := c.Ext(ctx, "x-unknown", nil, nil); CodeOf(err) != CodeBadRequest {
		t.Fatalf("unknown type: %v", err)
	}
	if _, err := c.Ext(ctx, "account", nil, nil); err == nil {
		t.Fatal("a built-in type sent as an extension")
	}

	// Payloads and replies far beyond the 2 KiB of other requests.
	raw, err = c.Ext(ctx, "x-test-big", nil, 600_000)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 600_002 {
		t.Fatalf("big reply: %d bytes", len(raw))
	}
	if _, err := c.Ext(ctx, "x-test-echo", nil, strings.Repeat("y", 500_000)); err != nil {
		t.Fatalf("big payload: %v", err)
	}
	if _, err := c.Ext(ctx, "x-test-echo", nil, strings.Repeat("y", MaxExtMsg)); err == nil {
		t.Fatal("payload over MaxExtMsg sent")
	}
}

func TestRegisterRequestNames(t *testing.T) {
	for _, bad := range []string{"account", "x", "relay-x", ""} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("registered %q", bad)
				}
			}()
			RegisterRequest(bad, nil)
		}()
	}
}

func TestRelayPolicyHook(t *testing.T) {
	as, logs := policyServer(t, RelayConfig{ForAccounts: true, Rate: 250_000, AccountRate: 1_000_000})
	ctx := tctx(t)
	teamDevice := newID(t)
	testRelayPolicy.mu.Lock()
	testRelayPolicy.member, testRelayPolicy.ended = teamDevice.Public(), nil
	testRelayPolicy.mu.Unlock()
	defer func() {
		testRelayPolicy.mu.Lock()
		testRelayPolicy.member = nil
		testRelayPolicy.mu.Unlock()
	}()
	hostID := as.relayHost(newID(t))

	// Not an account member, but the policy vouches for it: relayed on an
	// accounts-only server at the member rate.
	c := as.client()
	if _, err := c.RelayAs(ctx, as.introduce(c, hostID).Nonce, teamDevice); err != nil {
		t.Fatal(err)
	}
	waitLog(t, logs, "tier=account kbps=8000")
	// Anyone else is still refused.
	c2 := as.client()
	if _, err := c2.RelayAs(ctx, as.introduce(c2, hostID).Nonce, newID(t)); CodeOf(err) != CodeNoRelay {
		t.Fatalf("stranger relayed: %v", err)
	}
	// The session ends (nobody binds; idle) and the policy hears of it.
	deadline := time.Now().Add(5 * time.Second)
	for {
		testRelayPolicy.mu.Lock()
		ended := append([]endedRelay(nil), testRelayPolicy.ended...)
		testRelayPolicy.mu.Unlock()
		if len(ended) > 0 {
			if !bytes.Equal(ended[0].client, teamDevice.Public()) || ended[0].host == nil {
				t.Fatalf("ended: %+v", ended[0])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the relay policy never heard the session end")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
