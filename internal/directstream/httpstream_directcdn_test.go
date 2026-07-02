package directstream

import (
	"testing"
)

func TestDirectMode(t *testing.T) {
	proxy := &httpBaseStream{streamUrl: "https://cdn.example/server-link"}
	if proxy.directMode() {
		t.Fatal("no clientStreamUrl → proxy mode")
	}

	direct := &httpBaseStream{
		streamUrl:       "https://cdn.example/server-link",
		clientStreamUrl: "https://cdn.example/client-link",
	}
	if !direct.directMode() {
		t.Fatal("clientStreamUrl set → direct mode")
	}
}

// In direct mode the subtitle reader must be a chunked CDN reader over the SERVER link —
// it must not touch the FileStream cache (which the proxy never fills in direct mode).
func TestNewSubtitleReaderDirectMode(t *testing.T) {
	s := &httpBaseStream{
		streamUrl:       "https://cdn.example/server-link",
		clientStreamUrl: "https://cdn.example/client-link",
		BaseStream: BaseStream{
			manager: &Manager{},
		},
	}

	reader, err := s.newSubtitleReader()
	if err != nil {
		t.Fatalf("direct-mode subtitle reader: %v", err)
	}
	defer reader.Close()

	if reader == nil {
		t.Fatal("expected a reader")
	}
	// The FileStream cache must remain untouched (lazy chunked reader, no proxy fill).
	if s.httpStream != nil {
		t.Fatal("direct-mode subtitle reader must not initialize the FileStream cache")
	}
}
