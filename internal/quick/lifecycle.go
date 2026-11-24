package quick

import (
	"context"
	"time"
)

// life decides when a quick-support process whose window is gone should
// stop on its own. Nobody keeps a host running that they cannot see.
type life struct {
	// First is how long to wait for the first page to connect.
	First time.Duration
	// Bye is how long after the page said it was closing (a reload
	// reconnects within this time).
	Bye time.Duration
	// Lost is how long after the page vanished without a word: a crashed
	// browser, or one that suspended the tab. Sessions and pending
	// requests keep the host up for as long as they last, since the user did
	// not ask for them to end; running RemoHop again brings the window back.
	Lost time.Duration
}

var defaultLife = life{First: 2 * time.Minute, Bye: 3 * time.Second, Lost: 45 * time.Second}

// Reasons watch returns.
const (
	endNoWindow = "no window appeared"
	endClosed   = "the window was closed"
	endLost     = "the window is gone"
)

// watch returns the reason to stop once there is one, or "" when ctx ends.
// busy reports whether anybody is connected or waiting for an answer.
func (l life) watch(ctx context.Context, presence func() Presence, changed <-chan struct{}, busy func() bool) string {
	start := time.Now()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		switch p := presence(); {
		case p.Viewers > 0:
		case !p.Seen:
			if time.Since(start) > l.First {
				return endNoWindow
			}
		case p.Bye:
			if time.Since(p.Gone) > l.Bye {
				return endClosed
			}
		case time.Since(p.Gone) > l.Lost && !busy():
			return endLost
		}
		select {
		case <-ctx.Done():
			return ""
		case <-changed:
		case <-tick.C:
		}
	}
}
