package account

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/config"
	"github.com/readori/RemoHop/internal/signal"
)

// SettingTrust is the host setting that makes it accept its account's
// devices like authorised keys.
const SettingTrust = "account-trust"

// TrustFile records which authorised keys account-trust put in the host's
// paired.json, so that it only ever removes its own.
const TrustFile = "account-trust.json"

// trustPrefix names the authorised keys account-trust manages.
const trustPrefix = "account:"

// MaxStale is how long a host keeps trusting its account's devices without
// being able to fetch the account's log. A rendezvous server can withhold
// a removal from a host; past this the host trusts nobody from the account
// until it can check again.
const MaxStale = 7 * 24 * time.Hour

func init() {
	config.RegisterSetting(config.Setting{
		Key:      SettingTrust,
		Default:  "off",
		Validate: config.ValidateOnOff,
		Help: "on a host logged in to an account (`remohop login`): accept the account's other devices without a password, " +
			"like keys authorised with `remohop authorize`. Membership is checked against the account's signed device list, " +
			"which the rendezvous server cannot forge",
	})
}

type trustRecord struct {
	// Synced maps each key account-trust authorised to the ID of its
	// entry in paired.json.
	Synced map[string]string `json:"synced,omitempty"`
	// Denied are keys whose entry the host's owner revoked by hand; they
	// stay out until account-trust is turned off.
	Denied []string `json:"denied,omitempty"`
}

func loadTrust(path string) (*trustRecord, error) {
	tr := &trustRecord{Synced: map[string]string{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return tr, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, tr); err != nil {
		return nil, err
	}
	if tr.Synced == nil {
		tr.Synced = map[string]string{}
	}
	return tr, nil
}

// TrustEnabled reports whether account-trust is on in dir's config.
func TrustEnabled(dir string) bool {
	values, _ := config.AllIn(dir)
	return config.Bool(values[SettingTrust], false)
}

// SyncTrust brings the host's authorised keys in dir in line with its
// account, from the log this device last verified (it does not contact the
// server; RunHost does that). With account-trust off, not a member, or a
// log older than MaxStale, every key it added is removed. It reports how
// many keys were added and removed.
func SyncTrust(dir string) (added, removed int, err error) {
	path := filepath.Join(dir, TrustFile)
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) && !TrustEnabled(dir) {
		return 0, 0, nil // nothing was added and nothing is to be
	}
	unlock, err := config.Lock(path)
	if err != nil {
		return 0, 0, err
	}
	defer unlock()
	tr, err := loadTrust(path)
	if err != nil {
		return 0, 0, err
	}
	want := map[string]string{} // key → name
	on := TrustEnabled(dir)
	if on {
		if st, err := Load(dir); err == nil && st.Status == StatusMember && time.Since(st.Verified) < MaxStale {
			if ls, err := st.Ledger(); err == nil {
				self := mustIdentity(dir)
				names := map[string]string{}
				for _, d := range st.Devices {
					names[d.Key] = d.Name
				}
				for _, m := range ls.Members {
					if bytes.Equal(m.Key, self) {
						continue
					}
					k := auth.EncodeKey(m.Key)
					name := names[k]
					if name == "" {
						name = m.Name
					}
					if name == "" {
						name = auth.Fingerprint(m.Key)
					}
					want[k] = name
				}
			}
		}
	} else {
		tr.Denied = nil
	}
	tokens := auth.OpenTokenStore(filepath.Join(dir, auth.TokensFile))
	for k, tokID := range tr.Synced {
		ok, err := tokens.Exists(tokID)
		if err != nil {
			return added, removed, err
		}
		if !ok {
			// Revoked by the owner with `remohop host revoke`: respect that.
			delete(tr.Synced, k)
			if _, still := want[k]; still && !slices.Contains(tr.Denied, k) {
				tr.Denied = append(tr.Denied, k)
			}
			continue
		}
		if _, still := want[k]; !still {
			if _, err := tokens.Revoke(tokID); err != nil {
				return added, removed, err
			}
			delete(tr.Synced, k)
			removed++
		}
	}
	for k, name := range want {
		if _, done := tr.Synced[k]; done || slices.Contains(tr.Denied, k) {
			continue
		}
		key, _ := auth.DecodeKey(k)
		// A key its owner authorised by hand is left to the owner.
		if _, manual, err := tokens.AuthorizedKey(key); err != nil {
			return added, removed, err
		} else if manual {
			continue
		}
		tok, err := tokens.Authorize(trustPrefix+name, key)
		if err != nil {
			return added, removed, err
		}
		tr.Synced[k] = tok.ID
		added++
	}
	if len(tr.Synced) == 0 && len(tr.Denied) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return added, removed, err
		}
		return added, removed, nil
	}
	b, err := json.MarshalIndent(tr, "", "  ")
	if err != nil {
		return added, removed, err
	}
	return added, removed, config.WriteFileAtomic(path, append(b, '\n'), 0o600)
}

// Intervals of RunHost.
var (
	syncEvery = 5 * time.Second
	// refreshTrusting is how often a host with account-trust on fetches
	// the account's log; refreshIdle how often any logged-in host
	// reports in (its name, its platform), so the account shows it.
	refreshTrusting = time.Minute
	refreshIdle     = 10 * time.Minute
)

// RunHost keeps a host's account up to date for as long as ctx lasts: it
// reports the device's name to the account, fetches and verifies the log,
// and keeps account-trust's authorised keys in line (SyncTrust). It is
// meant to run beside `remohop host`. Registration itself needs nothing from
// here: the rendezvous server knows which registered keys belong to which
// account, so a host shows as online in its account as soon as it is.
func RunHost(ctx context.Context, dir string, log *slog.Logger) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	opts := Options{Dir: dir, Logger: log}
	if st, err := Load(dir); err == nil {
		log.Info("account: this host is in an account", "email", st.Email, "status", st.Status, "account_trust", TrustEnabled(dir))
	}
	var lastRefresh time.Time
	lastErr := ""
	t := time.NewTicker(syncEvery)
	defer t.Stop()
	for {
		st, err := Load(dir)
		if err == nil && st.Status != StatusLoggedOut {
			every := refreshIdle
			if TrustEnabled(dir) || st.Status == StatusPending {
				every = refreshTrusting
			}
			if time.Since(lastRefresh) >= every {
				lastRefresh = time.Now()
				_, err := Refresh(ctx, opts)
				msg := ""
				if err != nil {
					msg = err.Error()
				}
				if msg != lastErr {
					if err != nil {
						log.Warn("account: could not refresh the account", "err", err)
					} else if lastErr != "" {
						log.Info("account: refreshed the account again")
					}
					lastErr = msg
				}
				if signal.CodeOf(err) == signal.CodeNotLoggedIn {
					log.Warn("account: the rendezvous server no longer accepts this device's session; account-trust is suspended until `remohop login`")
				}
			}
		}
		if added, removed, err := SyncTrust(dir); err != nil {
			log.Warn("account: could not update account-trusted keys", "err", err)
		} else if added+removed > 0 {
			log.Info("account: account-trusted keys updated", "added", added, "removed", removed)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// IsTrustEntry reports whether an authorised key's name marks it as one
// account-trust manages.
func IsTrustEntry(name string) bool { return strings.HasPrefix(name, trustPrefix) }
