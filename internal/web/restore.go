package web

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/readori/RemoHop/internal/config"
)

// Restoring the host sessions of an app that ended (Config.Restore, the
// phone apps).
//
// A session lasts until the user disconnects it, however long the app was
// in the background (keep.go). But the system may end a suspended app
// altogether (iOS does when it needs the memory), and with it the gateway
// and its sessions. So the gateway keeps the list of its open sessions in
// openSessionsFile, and the next gateway logs in to those hosts again by
// itself: the page finds them "reconnecting" and opens the first, as after
// a drop. The file holds the hosts' targets only (an ID or host:port, as
// the saved-hosts file does), never a credential; logging in again needs
// one saved on this device (Config.Restore says which), so a session that
// was opened with a typed password only is not restored. A session leaves
// the list when the user ends it, and when it failed for good (the host's
// owner ended it, the credential was refused): restoring that would undo
// the host's choice.

// openSessionsFile, in ConfigDir, lists the open host sessions.
const openSessionsFile = "open-sessions.json"

// maxRestore bounds the sessions restored at start.
const maxRestore = 8

type openSession struct {
	Key    string `json:"key"`
	Target string `json:"target"`
}

// restoring is true when this gateway keeps and restores the list.
func (s *Server) restoring() bool {
	return s.cfg.Persistent && s.cfg.Restore != nil && s.cfg.ConfigDir != ""
}

var openMu sync.Mutex // one writer of the file at a time

// saveOpen writes the list of the gateway's open sessions: those not failed
// for good and not ended by the user. Not while the gateway is closing: an
// app that ends keeps the list for the next start.
func (s *Server) saveOpen() {
	if !s.restoring() || s.ctx.Err() != nil {
		return
	}
	// Held while the list is made, too: two saves at once then write in
	// the order they looked, so the last write is the newest list.
	openMu.Lock()
	defer openMu.Unlock()
	list := []openSession{}
	for _, p := range s.allPooled() {
		select {
		case <-p.stopped:
			continue
		default:
		}
		ci := p.snapshot()
		if ci.State == stateFailed || p.redial == nil || ci.Target == "" {
			continue
		}
		if !slices.ContainsFunc(list, func(o openSession) bool { return o.Key == ci.Key }) {
			list = append(list, openSession{Key: ci.Key, Target: ci.Target})
		}
	}
	b, err := json.Marshal(list)
	if err != nil {
		return
	}
	if err := config.WriteFileAtomic(filepath.Join(s.cfg.ConfigDir, openSessionsFile), b, 0o600); err != nil {
		s.log.Warn("web: could not save the open sessions", "err", err)
	}
}

// readOpen reads the list a previous gateway saved.
func (s *Server) readOpen() []openSession {
	b, err := os.ReadFile(filepath.Join(s.cfg.ConfigDir, openSessionsFile))
	if err != nil {
		return nil
	}
	var list []openSession
	if json.Unmarshal(b, &list) != nil {
		return nil
	}
	return list
}

// restore logs in again to the hosts of the sessions a previous gateway had
// open, each as a session of a browser session made for them, which the
// page's first browser session takes over (takeOver). Each starts as
// "reconnecting" and goes on as any session that dropped.
func (s *Server) restore() {
	if !s.restoring() {
		return
	}
	list := s.readOpen()
	if len(list) == 0 {
		return
	}
	s.mu.Lock()
	now := s.now()
	ws := &webSession{id: randToken(16), csrf: randToken(16), created: now, last: now,
		conns: map[string]*pooled{}, attempts: map[string]*attempt{}}
	s.sessions[ws.id] = ws
	s.mu.Unlock()
	n := 0
	for _, o := range list {
		if n >= maxRestore || o.Key == "" || o.Target == "" {
			continue
		}
		redial := s.cfg.Restore(o.Target)
		if redial == nil {
			s.log.Info("web: not restoring a session: no credential saved for its host", "target", o.Target)
			continue
		}
		release, aerr := s.reserve()
		if aerr != nil {
			break
		}
		p := newPooled(o.Key, nil, connInfo{Key: o.Key, Target: o.Target, Path: "unknown", State: stateReconnect})
		p.redial = redial
		s.mu.Lock()
		if s.ctx.Err() != nil {
			s.mu.Unlock()
			release()
			return
		}
		s.wg.Add(1)
		s.mu.Unlock()
		ws.mu.Lock()
		ws.conns[p.key] = p
		ws.mu.Unlock()
		n++
		s.log.Info("web: restoring a session the app had open", "target", o.Target)
		go func() {
			defer s.wg.Done()
			if !s.reconnect(p) {
				s.saveOpen()
				select {
				case <-p.stopped:
				case <-s.ctx.Done():
				}
				release()
				s.forget(p)
				p.close()
				s.saveOpen()
				return
			}
			s.keep(ws, p, release)
		}()
	}
}
