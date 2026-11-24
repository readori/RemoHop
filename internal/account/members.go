package account

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/readori/RemoHop/internal/account/ledger"
	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/signal"
)

// Devices lists the account's devices with their presence. Only devices
// in the verified log are listed, whatever the server says; ID, name,
// platform and presence are the server's word.
func Devices(ctx context.Context, opts Options) ([]Device, *State, error) {
	dir, err := opts.dir()
	if err != nil {
		return nil, nil, err
	}
	st, err := Load(dir)
	if err != nil {
		return nil, nil, err
	}
	if st.Status != StatusMember {
		return nil, st, notMember(dir, st)
	}
	r, err := call(ctx, opts, dir, st, "devices", signal.AccountArgs{})
	if err != nil {
		return nil, st, err
	}
	if err := apply(dir, st, r); err != nil {
		return nil, st, err
	}
	ls, err := st.Ledger()
	if err != nil {
		return nil, st, err
	}
	self := mustIdentity(dir)
	byKey := map[string]signal.AccountDevice{}
	for _, d := range r.Devices {
		byKey[string(d.Key)] = d
	}
	var out []Device
	for _, m := range ls.Members {
		d := Device{Name: m.Name, Identity: auth.Fingerprint(m.Key), Key: auth.EncodeKey(m.Key), This: bytes.Equal(m.Key, self)}
		if sd, ok := byKey[string(m.Key)]; ok {
			d.ID, d.Platform, d.Online = sd.ID, sd.Platform, sd.Online
			if sd.Name != "" {
				d.Name = sd.Name
			}
			if !sd.LastSeen.IsZero() {
				t := sd.LastSeen
				d.LastSeen = &t
			}
		}
		out = append(out, d)
	}
	st.Devices = out
	if err := commit(dir, st.Token, func(cur *State) error { cur.Devices = out; return nil }); err != nil {
		return nil, st, err
	}
	return out, st, nil
}

// CachedDevices is the device list as last fetched, without asking the
// server; presence is as it was then.
func CachedDevices(dir string) ([]Device, *State, error) {
	st, err := Load(dir)
	if err != nil {
		return nil, nil, err
	}
	ls, err := st.Ledger()
	if err != nil {
		return nil, st, nil
	}
	var out []Device
	for _, d := range st.Devices {
		if key, err := auth.DecodeKey(d.Key); err == nil && ls.IsMember(key) {
			d.Online = false
			out = append(out, d)
		}
	}
	return out, st, nil
}

func notMember(dir string, st *State) error {
	switch st.Status {
	case StatusPending:
		return fmt.Errorf("this device is waiting to be approved: on another device of the account run `remohop account approve %s`",
			auth.Fingerprint(mustIdentity(dir)))
	case StatusLoggedOut:
		return errors.New("the rendezvous server no longer accepts this device's account session; log in again with `remohop login`")
	}
	return ErrNotLoggedIn
}

// Pending lists the devices waiting for approval.
func Pending(ctx context.Context, opts Options) ([]JoinRequest, error) {
	_, pending, _, err := memberState(ctx, opts)
	return pending, err
}

// memberState fetches the state of the account for a member: the
// verified log and the join requests.
func memberState(ctx context.Context, opts Options) (*State, []JoinRequest, *ledger.State, error) {
	dir, err := opts.dir()
	if err != nil {
		return nil, nil, nil, err
	}
	st, err := Load(dir)
	if err != nil {
		return nil, nil, nil, err
	}
	if st.Status != StatusMember {
		return st, nil, nil, notMember(dir, st)
	}
	r, err := call(ctx, opts, dir, st, "state", signal.AccountArgs{})
	if err != nil {
		return st, nil, nil, err
	}
	if err := apply(dir, st, r); err != nil {
		return st, nil, nil, err
	}
	ls, err := st.Ledger()
	if err != nil {
		return st, nil, nil, err
	}
	var out []JoinRequest
	for _, p := range r.Pending {
		if ls.IsMember(p.Key) {
			continue
		}
		out = append(out, JoinRequest{Identity: auth.Fingerprint(p.Key), Key: auth.EncodeKey(p.Key), Name: p.Name,
			Platform: p.Platform, From: p.From, At: p.At})
	}
	return st, out, ls, nil
}

// Approve adds the waiting device whose identity fingerprint is exactly
// fingerprint. The fingerprint has to be the one the new device printed:
// that comparison is what keeps the rendezvous server from slipping its
// own key into the account.
func Approve(ctx context.Context, opts Options, fingerprint string) (*JoinRequest, error) {
	want := ledger.NormalizeFingerprint(fingerprint)
	if want == "" {
		return nil, fmt.Errorf("%q is not a complete identity fingerprint (20 hex digits as the new device prints them, "+
			"like 3f9a:01bc:77d2:e410:9c55)", fingerprint)
	}
	for attempt := 0; ; attempt++ {
		st, pending, ls, err := memberState(ctx, opts)
		if err != nil {
			return nil, err
		}
		var found *JoinRequest
		for i := range pending {
			if pending[i].Identity == want {
				found = &pending[i]
			}
		}
		if found == nil {
			return nil, fmt.Errorf("no device with identity %s is waiting to join the account (`remohop account` lists those that are)", want)
		}
		key, _ := auth.DecodeKey(found.Key)
		err = appendEntry(ctx, opts, st, ls, ledger.OpAdd, key, found.Name)
		if signal.CodeOf(err) == signal.CodeConflict && attempt == 0 {
			continue
		}
		return found, err
	}
}

func appendEntry(ctx context.Context, opts Options, st *State, ls *ledger.State, op string, key ed25519.PublicKey, name string) error {
	dir, err := opts.dir()
	if err != nil {
		return err
	}
	id, err := auth.LoadIdentity(dir)
	if err != nil {
		return err
	}
	e, err := ls.Next(id, op, key, name)
	if err != nil {
		return err
	}
	r, err := call(ctx, opts, dir, st, "append", signal.AccountArgs{Entry: &e})
	if err != nil {
		return err
	}
	if op == ledger.OpRemove && bytes.Equal(key, id.Public()) {
		return nil // the answer of a device that just left carries no log
	}
	return apply(dir, st, r)
}

// Reject drops the join request of the device with that fingerprint.
func Reject(ctx context.Context, opts Options, fingerprint string) error {
	want := ledger.NormalizeFingerprint(fingerprint)
	if want == "" {
		return fmt.Errorf("%q is not a complete identity fingerprint", fingerprint)
	}
	st, pending, _, err := memberState(ctx, opts)
	if err != nil {
		return err
	}
	for _, p := range pending {
		if p.Identity == want {
			key, _ := auth.DecodeKey(p.Key)
			dir, _ := opts.dir()
			_, err := call(ctx, opts, dir, st, "reject", signal.AccountArgs{Device: key})
			return err
		}
	}
	return fmt.Errorf("no device with identity %s is waiting to join the account", want)
}

// Remove takes a device out of the account: ref is its identity
// fingerprint, 9-digit ID or name (if unique). Its session ends, and hosts
// with account-trust stop accepting it once they see the change.
func Remove(ctx context.Context, opts Options, ref string) (*Device, error) {
	devices, st, err := Devices(ctx, opts)
	if err != nil {
		return nil, err
	}
	want := ledger.NormalizeFingerprint(ref)
	var match []Device
	for _, d := range devices {
		if (want != "" && d.Identity == want) || (ref != "" && (d.ID == strings.ReplaceAll(ref, " ", "") || d.Name == ref)) {
			match = append(match, d)
		}
	}
	switch len(match) {
	case 0:
		return nil, fmt.Errorf("no device %q in the account", ref)
	case 1:
	default:
		return nil, fmt.Errorf("%q names %d devices; use the identity fingerprint", ref, len(match))
	}
	key, _ := auth.DecodeKey(match[0].Key)
	dir, err := opts.dir()
	if err != nil {
		return nil, err
	}
	if err := removeDevice(ctx, opts, dir, st, key); err != nil {
		return nil, err
	}
	if match[0].This {
		if err := remove(dir); err != nil {
			return nil, err
		}
		SyncTrust(dir)
	}
	return &match[0], nil
}

// removeDevice appends a remove entry for key (nil: this device).
func removeDevice(ctx context.Context, opts Options, dir string, st *State, key ed25519.PublicKey) error {
	if key == nil {
		key = mustIdentity(dir)
	}
	for attempt := 0; ; attempt++ {
		r, err := call(ctx, opts, dir, st, "state", signal.AccountArgs{})
		if err != nil {
			return err
		}
		if err := apply(dir, st, r); err != nil {
			return err
		}
		ls, err := st.Ledger()
		if err != nil {
			return err
		}
		if !ls.IsMember(key) {
			return nil
		}
		err = appendEntry(ctx, opts, st, ls, ledger.OpRemove, key, "")
		if signal.CodeOf(err) == signal.CodeConflict && attempt == 0 {
			continue
		}
		return err
	}
}

// HostKey returns the identity key of the account device registered under
// id, if this device is a member and the device is in the verified log.
// The key is what a client pins for that host: the account's own
// devices vouch for it, not the rendezvous server. With refresh, a device
// that is not in the cached list is looked up on the server.
func HostKey(ctx context.Context, opts Options, id string, refresh bool) (ed25519.PublicKey, bool) {
	dir, err := opts.dir()
	if err != nil {
		return nil, false
	}
	find := func(devices []Device, st *State) (ed25519.PublicKey, bool) {
		ls, err := st.Ledger()
		if err != nil {
			return nil, false
		}
		self := mustIdentity(dir)
		for _, d := range devices {
			if d.ID != id {
				continue
			}
			key, err := auth.DecodeKey(d.Key)
			if err == nil && ls.IsMember(key) && !bytes.Equal(key, self) {
				return key, true
			}
		}
		return nil, false
	}
	st, err := Load(dir)
	if err != nil || st.Status != StatusMember {
		return nil, false
	}
	if key, ok := find(st.Devices, st); ok {
		return key, true
	}
	if !refresh {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	devices, st, err := Devices(ctx, opts)
	if err != nil {
		return nil, false
	}
	return find(devices, st)
}
