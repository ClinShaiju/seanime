package directstream

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/labstack/echo/v4"
)

// ServesStreamID reports whether this manager's currently-active stream has the
// given playback id. Used to route a serve request (which carries ?id=<playbackId>)
// to the right per-user manager when several users stream at once.
func (m *Manager) ServesStreamID(id string) bool {
	if id == "" {
		return false
	}
	m.playbackMu.Lock()
	defer m.playbackMu.Unlock()
	return m.currentPlaybackId == id
}

// ServeEchoStream is a proxy to the current stream.
// It sits in between the player and the real stream (whether it's a local file, torrent, or http stream).
//
// If this is an EBML stream, it gets the range request from the player, processes it to stream the correct subtitles, and serves the video.
// Otherwise, it just serves the video.
func (m *Manager) ServeEchoStream() http.Handler {
	return m.getStreamHandler()
}

// HasAttachment reports whether this manager's current stream contains the named
// attachment (font). Used to route a font request to the manager that actually owns the
// active stream when the request carries no user session or ?id=.
func (m *Manager) HasAttachment(filename string) bool {
	stream, ok := m.currentStream.Get()
	if !ok {
		return false
	}
	filename, _ = url.PathUnescape(filename)
	_, ok = stream.GetAttachmentByName(filename)
	return ok
}

// ServeEchoAttachments serves the attachments loaded into memory from the current stream.
func (m *Manager) ServeEchoAttachments(c echo.Context) error {
	// Get the current stream
	stream, ok := m.currentStream.Get()
	if !ok {
		return errors.New("no stream")
	}

	filename := c.Param("*")

	filename, _ = url.PathUnescape(filename)

	// Get the attachment
	attachment, ok := stream.GetAttachmentByName(filename)
	if !ok {
		return errors.New("attachment not found")
	}

	// Attachments (fonts) are immutable content. Let the client cache them so a binge of one
	// release — which reuses the same fonts every episode — only downloads each font once.
	// Correctness comes from the content-versioned URL (?cv=<size>) the client appends, so a
	// different font (different bytes/size) maps to a different cache key.
	c.Response().Header().Set("Cache-Control", "public, max-age=604800, immutable")

	return c.Blob(200, attachment.Mimetype, attachment.Data)
}
