package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeVsock imita o VirtioSocketConnection do vz: net.Conn sem CloseWrite,
// com o socket de verdade num campo privado rawConn.
type fakeVsock struct {
	net.Conn
	rawConn net.Conn
}

func unixPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	dir, err := os.MkdirTemp("", "nd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	ln, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	c, err := net.Dial("unix", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	s, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(); s.Close() })
	return c, s
}

func TestHalfCloserAchaRawConn(t *testing.T) {
	c, s := unixPair(t)
	v := &fakeVsock{rawConn: c}
	cw := halfCloser(v)
	if cw == nil {
		t.Fatal("deveria achar CloseWrite no rawConn")
	}
	if err := cw.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	_ = s.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := s.Read(make([]byte, 1)); err == nil {
		t.Fatal("o outro lado deveria ver EOF")
	}
}

func TestHalfCloserSemSuporte(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	if halfCloser(a) != nil {
		t.Fatal("net.Pipe não tem half-close")
	}
}
