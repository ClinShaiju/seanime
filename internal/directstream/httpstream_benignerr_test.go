package directstream

import (
	"context"
	"errors"
	"testing"
)

func TestIsBenignStreamWriteErr(t *testing.T) {
	benign := []error{
		context.Canceled,
		errors.New("write tcp 127.0.0.1:43211->127.0.0.1:44296: write: connection reset by peer"),
		errors.New("write tcp 127.0.0.1:43211->127.0.0.1:60634: write: broken pipe"),
		errors.New("stream error: stream ID 5; PROTOCOL_ERROR; received from peer"),
		errors.New("use of closed network connection"),
	}
	for _, err := range benign {
		if !isBenignStreamWriteErr(err) {
			t.Errorf("expected benign: %v", err)
		}
	}

	notBenign := []error{
		nil,
		errors.New("disk full"),
		errors.New("unexpected EOF from CDN"),
	}
	for _, err := range notBenign {
		if isBenignStreamWriteErr(err) {
			t.Errorf("expected NOT benign: %v", err)
		}
	}
}
