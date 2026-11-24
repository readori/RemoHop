package signal

import (
	"context"
	"crypto/ed25519"
	"errors"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/readori/RemoHop/internal/account/ledger"
	"github.com/readori/RemoHop/internal/auth"
)

// AccountReply is the server's answer to an account request. Nothing in it
// is trusted: the log must be verified (ledger.Verify) against the account
// ID the device holds, and the other fields are display data, already
// cleaned and bounded here.
type AccountReply struct {
	// State is AccountNew, AccountPending or AccountMember.
	State     string
	AccountID []byte
	// Token is the session token, from AccountVerify only.
	Token   []byte
	Log     []ledger.Entry
	Devices []AccountDevice
	Pending []AccountJoinRequest
}

// AccountDevice is a member device as the server sees it.
type AccountDevice struct {
	Key      ed25519.PublicKey
	ID       string // its 9-digit ID if it ever registered as a host here
	Name     string // what it last called itself
	Platform string
	Online   bool
	LastSeen time.Time // zero if never
}

// AccountJoinRequest is a device waiting to be approved.
type AccountJoinRequest struct {
	Key      ed25519.PublicKey
	Name     string
	Platform string
	From     string // the address it logged in from
	At       time.Time
}

// AccountArgs are the arguments of AccountCall.
type AccountArgs struct {
	Name     string        // this device's name, to update what the server shows
	Platform string        // this device's platform, likewise
	Entry    *ledger.Entry // append
	Device   ed25519.PublicKey
}

func (c *Client) accountRoundTrip(ctx context.Context, req request) (*AccountReply, error) {
	var resp response
	err := c.roundTrip(ctx, func(st *quic.Stream) error {
		if err := writeMsg(st, req); err != nil {
			return err
		}
		if err := readMsgN(st, &resp, maxAccountMsg); err != nil {
			return err
		}
		return resp.err()
	})
	if err != nil {
		return nil, err
	}
	return cleanAccountReply(resp.Account), nil
}

func cleanAccountReply(r *accountReply) *AccountReply {
	out := &AccountReply{}
	if r == nil {
		return out
	}
	switch r.State {
	case AccountNew, AccountPending, AccountMember:
		out.State = r.State
	}
	if len(r.AccountID) == 32 {
		out.AccountID = r.AccountID
	}
	if len(r.Token) == 32 {
		out.Token = r.Token
	}
	if len(r.Log) <= ledger.MaxEntries {
		out.Log = r.Log
	}
	for i, d := range r.Devices {
		if i == ledger.MaxMembers || len(d.Key) != ed25519.PublicKeySize {
			break
		}
		ad := AccountDevice{Key: d.Key, Name: auth.CleanText(d.Name, ledger.MaxName), Platform: cleanPlatform(d.Platform), Online: d.Online}
		if validID(d.ID) {
			ad.ID = d.ID
		}
		if d.LastSeen > 0 {
			ad.LastSeen = time.Unix(d.LastSeen, 0).UTC()
		}
		out.Devices = append(out.Devices, ad)
	}
	for i, p := range r.Pending {
		if i == maxPending || len(p.Key) != ed25519.PublicKeySize {
			break
		}
		out.Pending = append(out.Pending, AccountJoinRequest{Key: p.Key, Name: auth.CleanText(p.Name, ledger.MaxName),
			Platform: cleanPlatform(p.Platform), From: auth.CleanText(p.From, 64), At: time.Unix(p.At, 0).UTC()})
	}
	return out
}

func (c *Client) accountProof(id *auth.Identity) ([]byte, error) {
	cs := c.q.ConnectionState().TLS
	exp, err := cs.ExportKeyingMaterial(accountExporterLabel, nil, 32)
	if err != nil {
		return nil, err
	}
	return id.Sign(accountProofMessage(exp)), nil
}

// AccountLogin asks the server to mail a login code to email.
func (c *Client) AccountLogin(ctx context.Context, email string) error {
	var resp response
	return c.roundTrip(ctx, func(st *quic.Stream) error {
		if err := writeMsg(st, request{T: "account", V: protoVersion, Op: "login", Email: email}); err != nil {
			return err
		}
		if err := readMsg(st, &resp); err != nil {
			return err
		}
		return resp.err()
	})
}

// AccountVerify proves the login code and binds id's key to the account.
// The reply carries the session token.
func (c *Client) AccountVerify(ctx context.Context, id *auth.Identity, email, code, name, platform string) (*AccountReply, error) {
	proof, err := c.accountProof(id)
	if err != nil {
		return nil, err
	}
	r, err := c.accountRoundTrip(ctx, request{T: "account", V: protoVersion, Op: "verify", Email: email, Code: code,
		Key: id.Public(), Proof: proof, Name: name, Platform: platform})
	if err != nil {
		return nil, err
	}
	if r.Token == nil {
		return nil, errors.New("signal: login answer without a session")
	}
	return r, nil
}

// AccountCall runs an authenticated account operation: "state", "devices",
// "append", "reject", "logout" or "delete".
func (c *Client) AccountCall(ctx context.Context, id *auth.Identity, token []byte, op string, args AccountArgs) (*AccountReply, error) {
	proof, err := c.accountProof(id)
	if err != nil {
		return nil, err
	}
	return c.accountRoundTrip(ctx, request{T: "account", V: protoVersion, Op: op, Key: id.Public(), Token: token, Proof: proof,
		Name: args.Name, Platform: args.Platform, Entry: args.Entry, Device: args.Device})
}
