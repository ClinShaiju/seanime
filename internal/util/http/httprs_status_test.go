package httputil

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A non-2xx response must surface as a *StatusError (carrying the code), never as a reader
// over the error body — that swallowed path is what produced silent 0-track MKV parses.
func TestNewHttpReadSeekerFromURL_RejectsNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("rate limited"))
	}))
	defer srv.Close()

	r, err := NewHttpReadSeekerFromURLWithHeaders(srv.URL, nil)
	if r != nil {
		t.Fatal("expected nil reader on 429")
	}
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StatusError, got %v", err)
	}
	if se.Code != http.StatusTooManyRequests || se.RetryAfter != "3" {
		t.Fatalf("got code=%d retryAfter=%q", se.Code, se.RetryAfter)
	}
}

// A 2xx response still yields a working reader.
func TestNewHttpReadSeekerFromURL_OKReturnsReader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello"))
	}))
	defer srv.Close()

	r, err := NewHttpReadSeekerFromURLWithHeaders(srv.URL, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer r.Close()
	b, _ := io.ReadAll(r)
	if string(b) != "hello" {
		t.Fatalf("got body %q", string(b))
	}
}
