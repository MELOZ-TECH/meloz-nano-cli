package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEventsAppendAndFilter(t *testing.T) {
	p := filepath.Join(t.TempDir(), "events.log")
	logEvent(p, "daemon_start", map[string]any{"backend": "vz"})
	logEvent(p, "stop_request", map[string]any{"peer_pid": 42})
	logEvent(p, "daemon_exit", map[string]any{"signal": "killed"})

	all := readEvents(p, 10, "")
	if len(all) != 3 {
		t.Fatalf("esperava 3 eventos, veio %d", len(all))
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(all[2]), &rec); err != nil || rec["ev"] != "daemon_exit" || rec["signal"] != "killed" {
		t.Fatalf("último evento errado: %s", all[2])
	}
	if got := readEvents(p, 10, "stop_request"); len(got) != 1 || !strings.Contains(got[0], `"peer_pid":42`) {
		t.Fatalf("filtro por tipo falhou: %v", got)
	}
	if got := readEvents(p, 1, ""); len(got) != 1 || !strings.Contains(got[0], "daemon_exit") {
		t.Fatalf("limite falhou: %v", got)
	}
}

func TestEventsRotate(t *testing.T) {
	p := filepath.Join(t.TempDir(), "events.log")
	if err := os.WriteFile(p, bytes.Repeat([]byte("x"), eventsMax+1), 0o644); err != nil {
		t.Fatal(err)
	}
	logEvent(p, "daemon_start", nil)
	if _, err := os.Stat(p + ".1"); err != nil {
		t.Fatal("não girou para events.log.1")
	}
	if got := readEvents(p, 10, ""); len(got) != 1 {
		t.Fatalf("arquivo novo deveria ter 1 evento, tem %d", len(got))
	}
}

// do lado do servidor, o peer de uma conexão local é o próprio processo de teste
func TestPeerInfo(t *testing.T) {
	a, b := unixPair(t)
	defer a.Close()
	defer b.Close()
	info := peerInfo(b)
	if info["peer_pid"] != os.Getpid() {
		t.Fatalf("peer_pid = %v, esperava %d", info["peer_pid"], os.Getpid())
	}
}

func TestMCPInitializeAndList(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}
{"jsonrpc":"2.0","method":"notifications/initialized"}
{"jsonrpc":"2.0","id":2,"method":"tools/list"}
{"jsonrpc":"2.0","id":3,"method":"nada"}
`)
	var out bytes.Buffer
	if err := runMCP(in, &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("esperava 3 respostas (a notificação não responde), veio %d: %s", len(lines), out.String())
	}
	if !strings.Contains(lines[0], `"protocolVersion":"2025-06-18"`) {
		t.Fatalf("initialize: %s", lines[0])
	}
	for _, name := range []string{"status", "events", "daemon_log", "host_snapshot", "guest_run"} {
		if !strings.Contains(lines[1], `"name":"`+name+`"`) {
			t.Fatalf("tools/list sem %s", name)
		}
	}
	if !strings.Contains(lines[2], "-32601") {
		t.Fatalf("método desconhecido deveria dar -32601: %s", lines[2])
	}
}

func TestTailFileGrep(t *testing.T) {
	p := filepath.Join(t.TempDir(), "log")
	_ = os.WriteFile(p, []byte("a 1\nb 2\na 3\na 4\n"), 0o644)
	got, err := tailFile(p, 2, "a ")
	if err != nil || got != "a 3\na 4" {
		t.Fatalf("tailFile = %q, %v", got, err)
	}
}
