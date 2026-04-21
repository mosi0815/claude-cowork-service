package vm

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/patrickjaja/claude-cowork-service/process"
)

func TestRunPendingSdkInstallKeepsRequestWhenGuestDisconnected(t *testing.T) {
	b := NewKvmBackend("", false)
	pending := &pendingSdkInstall{sdkSubpath: "sdk/bin", version: "1.2.3"}
	b.pendingSdkInstall = pending

	b.runPendingSdkInstall()

	if b.pendingSdkInstall != pending {
		t.Fatalf("pending install was cleared without a successful guest forward")
	}
}

func TestRunPendingSdkInstallClearsRequestAfterSuccess(t *testing.T) {
	bridge, reader, writer := newConnectedTestBridge(t)
	defer func() { _ = reader.Close() }()
	defer func() { _ = writer.Close() }()

	b := NewKvmBackend("", false)
	b.bridge = bridge
	b.pendingSdkInstall = &pendingSdkInstall{sdkSubpath: "sdk/bin", version: "1.2.3"}

	done := make(chan struct{})
	go func() {
		b.runPendingSdkInstall()
		close(done)
	}()

	req := readGuestRequest(t, reader)
	var method string
	if err := json.Unmarshal(req["method"], &method); err != nil {
		t.Fatalf("unmarshal method: %v", err)
	}
	if method != "installSdk" {
		t.Fatalf("forwarded method = %q, want installSdk", method)
	}

	bridge.handleMessage(mustJSON(t, map[string]interface{}{
		"type":   "response",
		"id":     normalizeID(req["id"]),
		"result": map[string]bool{"ok": true},
	}))
	<-done

	if b.pendingSdkInstall != nil {
		t.Fatalf("pending install was not cleared after a successful guest forward")
	}
}

func TestEmitRemovesExitedProcessesFromRunningState(t *testing.T) {
	tests := []struct {
		name  string
		id    string
		event interface{}
	}{
		{
			name:  "guest exit map",
			id:    "guest-proc",
			event: map[string]interface{}{"type": "exit", "id": "guest-proc", "exitCode": float64(0)},
		},
		{
			name:  "local exit struct",
			id:    "local-proc",
			event: process.NewExitEvent("local-proc", 1),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := NewKvmBackend("", false)
			b.processes[tc.id] = struct{}{}

			b.emit(tc.event)

			running, err := b.IsProcessRunning(tc.id)
			if err != nil {
				t.Fatalf("IsProcessRunning: %v", err)
			}
			if running {
				t.Fatalf("process %q still marked running after exit event", tc.id)
			}
		})
	}
}

func newConnectedTestBridge(t *testing.T) (*GuestBridge, *os.File, *os.File) {
	t.Helper()

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}

	bridge := NewGuestBridge(VsockGuestPort, false, func(interface{}) {})
	bridge.conn = &vsockConn{file: writer}
	bridge.connected.Store(true)
	return bridge, reader, writer
}

func readGuestRequest(t *testing.T, reader *os.File) map[string]json.RawMessage {
	t.Helper()

	raw, err := readFramed(reader)
	if err != nil {
		t.Fatalf("readFramed: %v", err)
	}

	var req map[string]json.RawMessage
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("json.Unmarshal request: %v", err)
	}
	return req
}

func mustJSON(t *testing.T, payload interface{}) []byte {
	t.Helper()

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return raw
}
