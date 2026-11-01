package main

import (
	"net"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// TestPortForwardReusaConexao garante que as consultas periódicas ao Docker
// reaproveitam uma conexão em vez de abrir (e esquecer) uma nova a cada ciclo.
func TestPortForwardReusaConexao(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var novas atomic.Int64
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("[]"))
		}),
		ConnState: func(_ net.Conn, s http.ConnState) {
			if s == http.StateNew {
				novas.Add(1)
			}
		},
	}
	go srv.Serve(ln)
	defer srv.Close()

	p := newPortForward()
	for i := 0; i < 20; i++ {
		p.sync(sock)
	}
	if n := novas.Load(); n != 1 {
		t.Fatalf("20 consultas abriram %d conexões; esperava 1", n)
	}
}
