package events

import (
	"os"
	"seanime/internal/util"
	"seanime/internal/util/result"
	"sync"
	"time"

	"github.com/davecgh/go-spew/spew"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
)

type WSEventManagerInterface interface {
	SendEvent(t string, payload interface{})
	// SendEventToLoggedIn broadcasts like SendEvent, but on a networked (user-scoped) server it
	// skips anonymous (pre-login, UserID==0) connections — for pool-visible-but-not-public data
	// like watch-room discovery cards. Identical to SendEvent on local installs.
	SendEventToLoggedIn(t string, payload interface{})
	SendEventTo(clientId string, t string, payload interface{}, noLog ...bool)
	GetClientIds() []string
	GetClientPlatform(clientId string) string
	// GetConnUserID returns the Seanime user id associated with a connection
	// (clientId), for routing inbound client events to the owning user's modules.
	// ok is false when no connection with that id is registered.
	GetConnUserID(clientId string) (userID uint, ok bool)
	SubscribeToClientEvents(id string) *ClientEventSubscriber
	SubscribeToClientNativePlayerEvents(id string) *ClientEventSubscriber
	SubscribeToClientVideoCoreEvents(id string) *ClientEventSubscriber
	SubscribeToClientMpvCoreEvents(id string) *ClientEventSubscriber
	SubscribeToClientNakamaEvents(id string) *ClientEventSubscriber
	SubscribeToClientPlaylistEvents(id string) *ClientEventSubscriber
	UnsubscribeFromClientEvents(id string)
}

type GlobalWSEventManagerWrapper struct {
	WSEventManager WSEventManagerInterface
}

var GlobalWSEventManager *GlobalWSEventManagerWrapper

func (w *GlobalWSEventManagerWrapper) SendEvent(t string, payload interface{}) {
	if w.WSEventManager == nil {
		return
	}
	w.WSEventManager.SendEvent(t, payload)
}

func (w *GlobalWSEventManagerWrapper) SendEventTo(clientId string, t string, payload interface{}, noLog ...bool) {
	if w.WSEventManager == nil {
		return
	}
	w.WSEventManager.SendEventTo(clientId, t, payload, noLog...)
}

func (w *GlobalWSEventManagerWrapper) GetClientIds() []string {
	if w.WSEventManager == nil {
		return nil
	}
	return w.WSEventManager.GetClientIds()
}

type (
	// WSEventManager holds the websocket connection instance.
	// It is attached to the App instance, so it is available to other handlers.
	WSEventManager struct {
		Conns                              []*WSConn
		Logger                             *zerolog.Logger
		hasHadConnection                   bool
		// requireUserScoping is true on a password-protected (networked, multi-user)
		// server: there, a UserID==0 connection is an anonymous pre-login client, NOT the
		// local single user, so per-user events must not fan out to it. On a password-less
		// local/desktop install it stays false (UserID==0 is the legitimate sole user).
		requireUserScoping                 bool
		mu                                 sync.Mutex
		eventMu                            sync.RWMutex
		clientEventSubscribers             *result.Map[string, *ClientEventSubscriber]
		clientNativePlayerEventSubscribers *result.Map[string, *ClientEventSubscriber]
		clientVideoCoreEventSubscribers    *result.Map[string, *ClientEventSubscriber]
		clientMpvCoreEventSubscribers      *result.Map[string, *ClientEventSubscriber]
		nakamaEventSubscribers             *result.Map[string, *ClientEventSubscriber]
		playlistEventSubscribers           *result.Map[string, *ClientEventSubscriber]
	}

	ClientEventSubscriber struct {
		Channel chan *WebsocketClientEvent
		mu      sync.RWMutex
		closed  bool
	}

	WSConn struct {
		ID       string
		Platform string
		Conn     *websocket.Conn
		// UserID associates the connection with a Seanime user (multi-user event
		// scoping). 0 means unassociated (legacy / not logged in).
		UserID uint
	}

	WSEvent struct {
		Type    string      `json:"type"`
		Payload interface{} `json:"payload"`
	}
)

// NewWSEventManager creates a new WSEventManager instance for App.
func NewWSEventManager(logger *zerolog.Logger) *WSEventManager {
	ret := &WSEventManager{
		Logger:                             logger,
		Conns:                              make([]*WSConn, 0),
		clientEventSubscribers:             result.NewMap[string, *ClientEventSubscriber](),
		clientNativePlayerEventSubscribers: result.NewMap[string, *ClientEventSubscriber](),
		clientVideoCoreEventSubscribers:    result.NewMap[string, *ClientEventSubscriber](),
		clientMpvCoreEventSubscribers:      result.NewMap[string, *ClientEventSubscriber](),
		nakamaEventSubscribers:             result.NewMap[string, *ClientEventSubscriber](),
		playlistEventSubscribers:           result.NewMap[string, *ClientEventSubscriber](),
	}
	GlobalWSEventManager = &GlobalWSEventManagerWrapper{
		WSEventManager: ret,
	}
	return ret
}

// ExitIfNoConnsAsDesktopSidecar monitors the websocket connection as a desktop sidecar.
// It checks for a connection every 5 seconds. If a connection is lost, it starts a countdown a waits for 15 seconds.
// If a connection is not established within 15 seconds, it will exit the app.
func (m *WSEventManager) ExitIfNoConnsAsDesktopSidecar() {
	go func() {
		defer util.HandlePanicInModuleThen("events/ExitIfNoConnsAsDesktopSidecar", func() {})

		m.Logger.Info().Msg("ws: Monitoring connection as desktop sidecar")
		// Create a ticker to check connection every 5 seconds
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		// Track connection loss time
		var connectionLostTime time.Time
		exitTimeout := 10 * time.Second

		for range ticker.C {
			// Check WebSocket connection status
			if len(m.Conns) == 0 && m.hasHadConnection {
				// If not connected and first detection of connection loss
				if connectionLostTime.IsZero() {
					m.Logger.Warn().Msg("ws: No connection detected. Starting countdown...")
					connectionLostTime = time.Now()
				}

				// Check if connection has been lost for more than 15 seconds
				if time.Since(connectionLostTime) > exitTimeout {
					m.Logger.Warn().Msg("ws: No connection detected for 10 seconds. Exiting...")
					os.Exit(1)
				}
			} else {
				// Connection is active, reset connection lost time
				connectionLostTime = time.Time{}
			}
		}
	}()
}

func (m *WSEventManager) AddConn(id string, conn *websocket.Conn, platform ...string) {
	clientPlatform := ""
	if len(platform) > 0 {
		clientPlatform = platform[0]
	}

	m.hasHadConnection = true
	m.Conns = append(m.Conns, &WSConn{
		ID:       id,
		Platform: clientPlatform,
		Conn:     conn,
	})
}

// SetRequireUserScoping marks the server as password-protected (networked), so
// per-user events stop fanning out to anonymous UserID==0 connections. Set once at
// startup from cfg.Server.Password != "".
func (m *WSEventManager) SetRequireUserScoping(v bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requireUserScoping = v
}

// SetConnUserID associates a connection with a Seanime user, for per-user event
// scoping. Called once at upgrade when a session token is present.
func (m *WSEventManager) SetConnUserID(id string, userID uint) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, conn := range m.Conns {
		if conn.ID == id {
			conn.UserID = userID
		}
	}
}

// GetConnUserID returns the user id associated with a connection id, for routing
// inbound client (player) events to the module instance that owns that user.
func (m *WSEventManager) GetConnUserID(clientId string) (uint, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, conn := range m.Conns {
		if conn.ID == clientId {
			return conn.UserID, true
		}
	}
	return 0, false
}

// SendEventToUser sends an event to every connection belonging to the given user
// (a user may have several tabs/devices). Foundation for per-user event scoping;
// genuinely global events keep using SendEvent. No-op if userID is 0.
func (m *WSEventManager) SendEventToUser(userID uint, t string, payload interface{}) {
	if userID == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, conn := range m.Conns {
		if conn.UserID != userID {
			continue
		}
		_ = conn.Conn.WriteJSON(WSEvent{
			Type:    t,
			Payload: payload,
		})
	}
}

// SendEventToUserOrUnscoped sends an event to the given user's connections AND to
// any connection with no associated user (UserID==0). The latter covers local /
// password-less / desktop installs, where clients never present a session token and
// so are never tagged with a user id — they are effectively the single local user
// and must still receive scoped playback/stream events.
func (m *WSEventManager) SendEventToUserOrUnscoped(userID uint, t string, payload interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, conn := range m.Conns {
		// On a networked server, UserID==0 is an anonymous pre-login client — don't leak
		// the owner's per-user events (e.g. DebridStreamState carries the torrent name) to it.
		if conn.UserID != userID && (m.requireUserScoping || conn.UserID != 0) {
			continue
		}
		_ = conn.Conn.WriteJSON(WSEvent{
			Type:    t,
			Payload: payload,
		})
	}
}

// SendEventToIfOwner sends to the connection with clientId only if it belongs to
// the given owner user (or is an unidentified UserID==0 local client). It scopes a
// streaming module's client-targeted re-emit to the user who owns the active stream,
// so a different user's reconnecting client cannot pull the global playback state.
func (m *WSEventManager) SendEventToIfOwner(clientId string, ownerUserID uint, t string, payload interface{}, noLog ...bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, conn := range m.Conns {
		if conn.ID != clientId {
			continue
		}
		if conn.UserID != ownerUserID && conn.UserID != 0 {
			return // client belongs to another user — drop
		}
		_ = conn.Conn.WriteJSON(WSEvent{Type: t, Payload: payload})
		return
	}
}

func (m *WSEventManager) RemoveConn(id string) {
	for i, conn := range m.Conns {
		if conn.ID == id {
			m.Conns = append(m.Conns[:i], m.Conns[i+1:]...)
			break
		}
	}
}

// SendEvent sends a websocket event to the client.
func (m *WSEventManager) SendEvent(t string, payload interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// If there's no connection, do nothing
	//if m.Conn == nil {
	//	return
	//}

	if t != PlaybackManagerProgressPlaybackState && payload == nil {
		m.Logger.Trace().Str("type", t).Msg("ws: Sending message")
	}

	for _, conn := range m.Conns {
		err := conn.Conn.WriteJSON(WSEvent{
			Type:    t,
			Payload: payload,
		})
		if err != nil {
			// Note: NaN error coming from [progress_tracking.go]
			//m.Logger.Err(err).Msg("ws: Failed to send message")
		}
		//m.Logger.Trace().Str("type", t).Msg("ws: Sent message")
	}

	//err := m.Conn.WriteJSON(WSEvent{
	//	Type:    t,
	//	Payload: payload,
	//})
	//if err != nil {
	//	m.Logger.Err(err).Msg("ws: Failed to send message")
	//}
	//m.Logger.Trace().Str("type", t).Msg("ws: Sent message")
}

// SendEventToLoggedIn broadcasts to every connection except, on a networked (user-scoped)
// server, anonymous pre-login ones (UserID==0). Local installs (no server password) tag no
// connections, so there it behaves exactly like SendEvent.
func (m *WSEventManager) SendEventToLoggedIn(t string, payload interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, conn := range m.Conns {
		if m.requireUserScoping && conn.UserID == 0 {
			continue
		}
		_ = conn.Conn.WriteJSON(WSEvent{Type: t, Payload: payload})
	}
}

// SendEventTo sends a websocket event to the specified client.
func (m *WSEventManager) SendEventTo(clientId string, t string, payload interface{}, noLog ...bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, conn := range m.Conns {
		if conn.ID == clientId {
			if t != "pong" {
				if len(noLog) == 0 || !noLog[0] {
					truncated := spew.Sprint(payload)
					if len(truncated) > 500 {
						truncated = truncated[:500] + "..."
					}
					m.Logger.Trace().Str("to", clientId).Str("type", t).Str("payload", truncated).Msg("ws: Sending message")
				}
			}
			_ = conn.Conn.WriteJSON(WSEvent{
				Type:    t,
				Payload: payload,
			})
		}
	}
}

func (m *WSEventManager) SendStringTo(clientId string, s string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, conn := range m.Conns {
		if conn.ID == clientId {
			_ = conn.Conn.WriteMessage(websocket.TextMessage, []byte(s))
		}
	}
}

func (m *WSEventManager) GetClientIds() []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	ret := make([]string, 0, len(m.Conns))
	for _, conn := range m.Conns {
		if conn == nil || conn.ID == "" {
			continue
		}
		ret = append(ret, conn.ID)
	}

	return ret
}

func (m *WSEventManager) GetClientPlatform(clientId string) string {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, conn := range m.Conns {
		if conn == nil || conn.ID != clientId {
			continue
		}
		return conn.Platform
	}

	return ""
}

func (m *WSEventManager) OnClientEvent(event *WebsocketClientEvent) {
	m.eventMu.RLock()
	defer m.eventMu.RUnlock()

	onEvent := func(key string, subscriber *ClientEventSubscriber) bool {
		go func() {
			defer util.HandlePanicInModuleThen("events/OnClientEvent/clientNativePlayerEventSubscribers", func() {})
			subscriber.mu.RLock()
			defer subscriber.mu.RUnlock()
			if !subscriber.closed {
				select {
				case subscriber.Channel <- event:
				default:
					// Channel is blocked, skip sending
					m.Logger.Warn().Msgf("ws: Client event channel is blocked, event dropped, %v", subscriber)
				}
			}
		}()
		return true
	}

	switch event.Type {
	case NativePlayerEventType:
		m.clientNativePlayerEventSubscribers.Range(onEvent)
	case VideoCoreEventType:
		m.clientVideoCoreEventSubscribers.Range(onEvent)
	case MpvCoreEventType:
		m.clientMpvCoreEventSubscribers.Range(onEvent)
	case NakamaEventType:
		m.nakamaEventSubscribers.Range(onEvent)
	case PlaylistEvent:
		m.playlistEventSubscribers.Range(onEvent)
	default:
		m.clientEventSubscribers.Range(onEvent)
	}
}

func (m *WSEventManager) SubscribeToClientEvents(id string) *ClientEventSubscriber {
	subscriber := &ClientEventSubscriber{
		Channel: make(chan *WebsocketClientEvent, 900),
	}
	m.clientEventSubscribers.Set(id, subscriber)
	return subscriber
}

func (m *WSEventManager) SubscribeToClientNativePlayerEvents(id string) *ClientEventSubscriber {
	subscriber := &ClientEventSubscriber{
		Channel: make(chan *WebsocketClientEvent, 100),
	}
	m.clientNativePlayerEventSubscribers.Set(id, subscriber)
	return subscriber
}

func (m *WSEventManager) SubscribeToClientVideoCoreEvents(id string) *ClientEventSubscriber {
	subscriber := &ClientEventSubscriber{
		Channel: make(chan *WebsocketClientEvent, 100),
	}
	m.clientVideoCoreEventSubscribers.Set(id, subscriber)
	return subscriber
}

func (m *WSEventManager) SubscribeToClientMpvCoreEvents(id string) *ClientEventSubscriber {
	subscriber := &ClientEventSubscriber{
		Channel: make(chan *WebsocketClientEvent, 100),
	}
	m.clientMpvCoreEventSubscribers.Set(id, subscriber)
	return subscriber
}

func (m *WSEventManager) SubscribeToClientNakamaEvents(id string) *ClientEventSubscriber {
	subscriber := &ClientEventSubscriber{
		Channel: make(chan *WebsocketClientEvent, 100),
	}
	m.nakamaEventSubscribers.Set(id, subscriber)
	return subscriber
}

func (m *WSEventManager) SubscribeToClientPlaylistEvents(id string) *ClientEventSubscriber {
	subscriber := &ClientEventSubscriber{
		Channel: make(chan *WebsocketClientEvent, 100),
	}
	m.playlistEventSubscribers.Set(id, subscriber)
	return subscriber
}

func (m *WSEventManager) UnsubscribeFromClientEvents(id string) {
	m.eventMu.Lock()
	defer m.eventMu.Unlock()
	defer func() {
		if r := recover(); r != nil {
			m.Logger.Warn().Msg("ws: Failed to unsubscribe from client events")
		}
	}()
	maps := []*result.Map[string, *ClientEventSubscriber]{
		m.clientEventSubscribers,
		m.clientNativePlayerEventSubscribers,
		m.clientVideoCoreEventSubscribers,
		m.clientMpvCoreEventSubscribers,
		m.nakamaEventSubscribers,
		m.playlistEventSubscribers,
	}
	for _, subscribers := range maps {
		subscriber, ok := subscribers.Pop(id)
		if !ok {
			continue
		}
		subscriber.mu.Lock()
		subscriber.closed = true
		close(subscriber.Channel)
		subscriber.mu.Unlock()
		return
	}
}
