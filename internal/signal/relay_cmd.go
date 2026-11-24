package signal

import (
	"errors"
	"fmt"
	"io"
	"os"
	ossignal "os/signal"
	"strings"
	"syscall"
	"time"
)

func checkRelayPolicyFlags(perSource, accountKbps int, relayFor string, monthlyGB int64, accounts bool) error {
	switch {
	case perSource < 0:
		return errors.New("--relay-per-source must be positive (0 for no limit)")
	case accountKbps < 0:
		return errors.New("--relay-account-kbps must be positive")
	case relayFor != "all" && relayFor != "accounts":
		return fmt.Errorf("--relay-for %q: use all or accounts", relayFor)
	case monthlyGB < 0:
		return errors.New("--relay-monthly-gb must be positive (0 for no budget)")
	case !accounts && (relayFor == "accounts" || accountKbps != 0):
		return errors.New("--relay-for accounts and --relay-account-kbps need --accounts: membership is checked against this server's accounts")
	}
	return nil
}

// relayPolicyLine continues the startup "relay: on (..." line.
func relayPolicyLine(rc *RelayConfig, srv *Server) string {
	var b strings.Builder
	if rc.PerSource > 0 && rc.PerSource < int(^uint(0)>>1) {
		fmt.Fprintf(&b, ", %d per address", rc.PerSource)
	}
	if rc.AccountRate != rc.Rate {
		fmt.Fprintf(&b, ", %d kbit/s for account devices", rc.AccountRate/125)
	}
	if rc.ForAccounts {
		b.WriteString(", account devices only")
	}
	if rc.DenyFile != "" {
		fmt.Fprintf(&b, ", deny list %s", rc.DenyFile)
	}
	if u := srv.relayUsage; u != nil {
		fmt.Fprintf(&b, ", %s GB relayed this month", gb(u.current()))
		if u.budget > 0 {
			fmt.Fprintf(&b, " of %s GB", gb(u.budget))
		}
	}
	return b.String()
}

// printRelayStatus reports this month's relay usage from the data
// directory (for `remohop signal --status`).
func printRelayStatus(w io.Writer, dir string, budget int64, now time.Time) error {
	r, err := readUsage(dir)
	if err != nil {
		return err
	}
	month := monthOf(now)
	used := int64(0)
	if r.Month == month {
		used = r.Bytes
	}
	fmt.Fprintf(w, "relay usage %s (UTC): %s GB (%d bytes)\n", month, gb(used), used)
	if r.Month != "" && r.Month != month {
		fmt.Fprintf(w, "  last recorded month %s: %s GB\n", r.Month, gb(r.Bytes))
	}
	if r.Month == "" {
		fmt.Fprintf(w, "  (no %s in %s: the relay has not run with this data directory)\n", usageFile, dir)
	}
	if budget > 0 {
		u := &relayUsage{budget: budget}
		state := "normal"
		switch u.level(used) {
		case 80:
			state = "anonymous sessions at half rate"
		case 100:
			state = "anonymous sessions refused"
		case 110:
			state = "all new sessions refused"
		}
		fmt.Fprintf(w, "  budget %s GB: %.1f%% used, %s\n", gb(budget), float64(used)*100/float64(budget), state)
	}
	fmt.Fprintln(w, "  (written about once a minute by the running server)")
	return nil
}

// onHangup calls fn on every SIGHUP until the returned stop is called.
// On Windows there is no SIGHUP and fn is never called.
func onHangup(fn func()) (stop func()) {
	ch := make(chan os.Signal, 1)
	ossignal.Notify(ch, syscall.SIGHUP)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ch:
				fn()
			case <-done:
				return
			}
		}
	}()
	return func() {
		ossignal.Stop(ch)
		close(done)
	}
}
