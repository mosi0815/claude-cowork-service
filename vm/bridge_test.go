package vm

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGuestBridgeForwardReturnsGuestError(t *testing.T) {
	bridge, reader, writer := newConnectedTestBridge(t)
	defer func() { _ = reader.Close() }()
	defer func() { _ = writer.Close() }()

	errCh := make(chan error, 1)
	go func() {
		_, err := bridge.Forward("deleteSessionDirs", map[string]interface{}{
			"names": []string{"stale-session"},
		})
		errCh <- err
	}()

	req := readGuestRequest(t, reader)
	bridge.handleMessage(mustJSON(t, map[string]interface{}{
		"type":  "response",
		"id":    normalizeID(req["id"]),
		"error": "guest refused delete",
	}))

	err := <-errCh
	if err == nil {
		t.Fatalf("Forward returned nil error for guest failure response")
	}
	if !strings.Contains(err.Error(), "guest refused delete") {
		t.Fatalf("Forward error = %q, want guest error text", err.Error())
	}
}

func TestGuestResponseErrorHandlesStructuredPayloads(t *testing.T) {
	raw, err := json.Marshal(map[string]string{"message": "broken"})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	got := guestResponseError(raw)
	if got == nil {
		t.Fatalf("guestResponseError returned nil for structured guest error")
	}
	if !strings.Contains(got.Error(), "broken") {
		t.Fatalf("guestResponseError = %q, want JSON payload text", got.Error())
	}
}
