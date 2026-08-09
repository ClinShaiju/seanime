package events

import (
	"testing"

	"seanime/internal/util"

	"github.com/stretchr/testify/require"
)

func TestWSEventManagerGetClientPlatform(t *testing.T) {
	manager := NewWSEventManager(util.NewLogger())

	manager.AddConn("web-client", nil)
	manager.AddConn("denshi-client", nil, "denshi")

	require.Empty(t, manager.GetClientPlatform("web-client"))
	require.Equal(t, "denshi", manager.GetClientPlatform("denshi-client"))
	require.Empty(t, manager.GetClientPlatform("missing-client"))
}

// clientId is persisted in localStorage, so concurrent tabs of the same browser share
// one id. Removal must therefore target the socket that actually closed, not the first
// entry matching its id — otherwise closing one tab silently unhooks the other.
func TestWSEventManagerRemoveConnByIdentity(t *testing.T) {
	manager := NewWSEventManager(util.NewLogger())

	tab1 := manager.AddConn("shared-id", nil)
	tab2 := manager.AddConn("shared-id", nil)

	manager.RemoveConn(tab2)

	// tab1 is still live and must still be registered under the shared id.
	require.Equal(t, []string{"shared-id"}, manager.GetClientIds())
	require.Len(t, manager.Conns, 1)
	require.Same(t, tab1, manager.Conns[0])

	manager.RemoveConn(tab1)
	require.Empty(t, manager.GetClientIds())

	// Removing an already-removed or unknown conn is a no-op, not a panic.
	manager.RemoveConn(tab1)
	manager.RemoveConn(nil)
	require.Empty(t, manager.GetClientIds())
}

// A client that reconnects reuses its persisted id; the new socket is a distinct
// *WSConn, so removing the old one must leave the reconnected client registered.
func TestWSEventManagerReconnectSameID(t *testing.T) {
	manager := NewWSEventManager(util.NewLogger())

	old := manager.AddConn("denshi-client", nil, "denshi")
	reconnected := manager.AddConn("denshi-client", nil, "denshi")

	// The old socket's read loop errors out after the reconnect and removes itself.
	manager.RemoveConn(old)

	require.Equal(t, []string{"denshi-client"}, manager.GetClientIds())
	require.Same(t, reconnected, manager.Conns[0])
	require.Equal(t, "denshi", manager.GetClientPlatform("denshi-client"))
}
