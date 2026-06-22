package events

import "sync/atomic"

// OwnerScopedWSEventManager wraps the real WSEventManager and rebinds broadcast
// SendEvent calls to the current "stream owner" — the user who started the active
// playback/stream. The streaming/playback modules (which are shared singletons and
// run one active stream at a time) are constructed with this wrapper, so their
// existing broadcast SendEvent calls only reach the owning user instead of every
// connected client. The owner is (re)set at each stream/playback start.
//
// ponytail: this scopes events for the *single active stream* the server runs
// today. True simultaneous independent streams need per-session module instances
// (the larger streaming split); until then one owner at a time is correct because
// only one stream is active server-wide.
//
// Client-targeted methods (SendEventTo, Subscribe*) are already client-scoped and
// pass straight through.
type OwnerScopedWSEventManager struct {
	inner *WSEventManager
	owner atomic.Uint64 // 0 = broadcast (no active owner)
}

func NewOwnerScopedWSEventManager(inner *WSEventManager) *OwnerScopedWSEventManager {
	return &OwnerScopedWSEventManager{inner: inner}
}

// SetOwner records the user who owns the currently-active stream. 0 clears it
// (events broadcast again).
func (s *OwnerScopedWSEventManager) SetOwner(userID uint) {
	s.owner.Store(uint64(userID))
}

func (s *OwnerScopedWSEventManager) SendEvent(t string, payload interface{}) {
	owner := s.owner.Load()
	if owner == 0 {
		s.inner.SendEvent(t, payload)
		return
	}
	// Deliver to the owner's clients, plus any unidentified (UserID==0) clients so
	// local / password-less / desktop installs (which never set a conn user id) keep
	// receiving their own playback events.
	s.inner.SendEventToUserOrUnscoped(uint(owner), t, payload)
}

func (s *OwnerScopedWSEventManager) SendEventTo(clientId string, t string, payload interface{}, noLog ...bool) {
	s.inner.SendEventTo(clientId, t, payload, noLog...)
}

func (s *OwnerScopedWSEventManager) GetClientIds() []string {
	return s.inner.GetClientIds()
}

func (s *OwnerScopedWSEventManager) GetClientPlatform(clientId string) string {
	return s.inner.GetClientPlatform(clientId)
}

func (s *OwnerScopedWSEventManager) SubscribeToClientEvents(id string) *ClientEventSubscriber {
	return s.inner.SubscribeToClientEvents(id)
}

func (s *OwnerScopedWSEventManager) SubscribeToClientNativePlayerEvents(id string) *ClientEventSubscriber {
	return s.inner.SubscribeToClientNativePlayerEvents(id)
}

func (s *OwnerScopedWSEventManager) SubscribeToClientVideoCoreEvents(id string) *ClientEventSubscriber {
	return s.inner.SubscribeToClientVideoCoreEvents(id)
}

func (s *OwnerScopedWSEventManager) SubscribeToClientNakamaEvents(id string) *ClientEventSubscriber {
	return s.inner.SubscribeToClientNakamaEvents(id)
}

func (s *OwnerScopedWSEventManager) SubscribeToClientPlaylistEvents(id string) *ClientEventSubscriber {
	return s.inner.SubscribeToClientPlaylistEvents(id)
}

func (s *OwnerScopedWSEventManager) UnsubscribeFromClientEvents(id string) {
	s.inner.UnsubscribeFromClientEvents(id)
}
