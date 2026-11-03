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

func TestDaemonAlive(t *testing.T) {
	ctl := filepath.Join(t.TempDir(), "nanoc.sock")
	if daemonAlive(ctl) {
		t.Fatal("sem arquivo de lock não há daemon")
	}
	if err := lockDaemon(ctl); err != nil {
		t.Fatal(err)
	}
	if !daemonAlive(ctl) {
		t.Fatal("lock preso deveria indicar daemon vivo")
	}
	daemonLock.Close()
	daemonLock = nil
	if daemonAlive(ctl) {
		t.Fatal("lock solto deveria indicar daemon morto")
	}
}

func TestGrowDataImage(t *testing.T) {
	t.Setenv("NANOC_DISK_GB", "16")
	p := filepath.Join(t.TempDir(), "data.img")
	if created, err := ensureDataImage(p); err != nil || !created {
		t.Fatalf("ensureDataImage: %v %v", created, err)
	}
	if st, _ := os.Stat(p); st.Size() != 16<<30 {
		t.Fatalf("tamanho %d", st.Size())
	}
	if grown, err := growDataImage(p); err != nil || grown {
		t.Fatalf("já tinha o tamanho: %v %v", grown, err)
	}
	t.Setenv("NANOC_DISK_GB", "32")
	if grown, err := growDataImage(p); err != nil || !grown {
		t.Fatalf("devia crescer: %v %v", grown, err)
	}
	t.Setenv("NANOC_DISK_GB", "8")
	if grown, _ := growDataImage(p); grown {
		t.Fatal("nunca diminui")
	}
	if st, _ := os.Stat(p); st.Size() != 32<<30 {
		t.Fatalf("tamanho %d", st.Size())
	}
}
