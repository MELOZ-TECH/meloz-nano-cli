package main

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// backend imita o dockerd: lê a requisição até o EOF, responde e só então fecha.
// Sem o half-close correto no relay, ele nunca vê o EOF e a conexão fica aberta.
func startBackend(t *testing.T) (string, *atomic.Int64) {
	t.Helper()
	dir, err := os.MkdirTemp("", "nr") // caminho curto: unix socket no macOS tem limite de 104 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var open atomic.Int64
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			open.Add(1)
			go func(c net.Conn) {
				defer open.Add(-1)
				defer c.Close()
				req, _ := io.ReadAll(c)
				_, _ = c.Write(append([]byte("resp:"), req...))
			}(c)
		}
	}()
	return sock, &open
}

func TestRelayHalfCloseLiberaBackend(t *testing.T) {
	sock, open := startBackend(t)

	front, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer front.Close()
	go func() {
		for {
			c, err := front.Accept()
			if err != nil {
				return
			}
			up, err := net.Dial("unix", sock)
			if err != nil {
				c.Close()
				continue
			}
			go relay(c, up)
		}
	}()

	const n = 200
	for i := 0; i < n; i++ {
		c, err := net.Dial("tcp", front.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		_, _ = c.Write([]byte("ping"))
		_ = c.(*net.TCPConn).CloseWrite()
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		got, err := io.ReadAll(c)
		c.Close()
		if err != nil || string(got) != "resp:ping" {
			t.Fatalf("requisição %d: resposta %q, erro %v", i, got, err)
		}
	}

	deadline := time.Now().Add(2 * time.Second)
	for open.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if v := open.Load(); v != 0 {
		t.Fatalf("%d conexões com o backend ficaram abertas (vazamento)", v)
	}
}

func TestRelayTerminaQuandoClienteFecha(t *testing.T) {
	sock, _ := startBackend(t)
	a, b := net.Pipe() // sem CloseWrite: cai no caminho de fechar tudo
	up, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { relay(b, up); close(done) }()
	a.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("relay não terminou depois que o cliente fechou")
	}
}

func TestReadAddr(t *testing.T) {
	for in, ok := range map[string]bool{
		"172.17.0.2:80\nresto": true,
		"10.0.0.5:5432\n":      true,
		"8.8.8.8:53\n":         false,
		"127.0.0.1:22\n":       false,
		"172.17.0.2:0\n":       false,
		"172.17.0.2:80":        false,
	} {
		r := strings.NewReader(in)
		got, err := readAddr(r)
		if (err == nil) != ok {
			t.Errorf("%q: %q %v", in, got, err)
		}
		if ok {
			rest, _ := io.ReadAll(r)
			if want := in[strings.IndexByte(in, '\n')+1:]; string(rest) != want {
				t.Errorf("%q: sobrou %q, quero %q", in, rest, want)
			}
		}
	}
}
