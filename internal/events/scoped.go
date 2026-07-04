package events

// ScopedWSEventManager binds a per-user module instance to a single user: every
// SendEvent / SendEventTo it makes reaches only that user's clients (plus
// unidentified UserID==0 local clients, so local/desktop installs keep working).
// Each per-session streaming/playback module emits to its own fixed user, which is
// what lets two users stream simultaneously without their events crossing.
// (The App-global modules use one of these fixed to the admin user.)
type ScopedWSEventManager struct {
	inner  *WSEventManager
	userID uint
}

func NewScopedWSEventManager(inner *WSEventManager, userID uint) *ScopedWSEventManager {
	return &ScopedWSEventManager{inner: inner, userID: userID}
}

func (s *ScopedWSEventManager) SendEvent(t string, payload interface{}) {
	s.inner.SendEventToUserOrUnscoped(s.userID, t, payload)
}

// SendEventToLoggedIn on a user-scoped manager stays scoped to the owner — the "all logged-in
// users" semantic only exists on the global manager (pool-wide broadcasts go through it).
func (s *ScopedWSEventManager) SendEventToLoggedIn(t string, payload interface{}) {
	s.SendEvent(t, payload)
}

func (s *ScopedWSEventManager) SendEventTo(clientId string, t string, payload interface{}, noLog ...bool) {
	s.inner.SendEventToIfOwner(clientId, s.userID, t, payload, noLog...)
}

func (s *ScopedWSEventManager) GetClientIds() []string { return s.inner.GetClientIds() }
func (s *ScopedWSEventManager) GetConnUserID(clientId string) (uint, bool) {
	return s.inner.GetConnUserID(clientId)
}
func (s *ScopedWSEventManager) GetClientPlatform(clientId string) string {
	return s.inner.GetClientPlatform(clientId)
}
func (s *ScopedWSEventManager) SubscribeToClientEvents(id string) *ClientEventSubscriber {
	return s.inner.SubscribeToClientEvents(id)
}
func (s *ScopedWSEventManager) SubscribeToClientNativePlayerEvents(id string) *ClientEventSubscriber {
	return s.inner.SubscribeToClientNativePlayerEvents(id)
}
func (s *ScopedWSEventManager) SubscribeToClientVideoCoreEvents(id string) *ClientEventSubscriber {
	return s.inner.SubscribeToClientVideoCoreEvents(id)
}
func (s *ScopedWSEventManager) SubscribeToClientMpvCoreEvents(id string) *ClientEventSubscriber {
	return s.inner.SubscribeToClientMpvCoreEvents(id)
}
func (s *ScopedWSEventManager) SubscribeToClientNakamaEvents(id string) *ClientEventSubscriber {
	return s.inner.SubscribeToClientNakamaEvents(id)
}
func (s *ScopedWSEventManager) SubscribeToClientPlaylistEvents(id string) *ClientEventSubscriber {
	return s.inner.SubscribeToClientPlaylistEvents(id)
}
func (s *ScopedWSEventManager) UnsubscribeFromClientEvents(id string) {
	s.inner.UnsubscribeFromClientEvents(id)
}
