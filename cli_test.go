package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnvOr(t *testing.T) {
	t.Setenv("NANOC_TEST_KEY", "")
	if got := envOr("NANOC_TEST_KEY", "fallback"); got != "fallback" {
		t.Fatalf("vazio deveria cair no default, veio %q", got)
	}
	t.Setenv("NANOC_TEST_KEY", "valor")
	if got := envOr("NANOC_TEST_KEY", "fallback"); got != "valor" {
		t.Fatalf("esperava valor, veio %q", got)
	}
}

func TestMemBytes(t *testing.T) {
	t.Setenv("NANOC_MEM_MB", "")
	def := memBytes()
	if def < 2048*mib || def > 8192*mib {
		t.Fatalf("teto padrão deveria ficar entre 2 e 8 GiB, veio %d", def)
	}
	t.Setenv("NANOC_MEM_MB", "2048")
	if got := memBytes(); got != 2048*mib {
		t.Fatalf("esperava 2 GiB, veio %d", got)
	}
	t.Setenv("NANOC_MEM_MB", "invalido")
	if got := memBytes(); got != def {
		t.Fatalf("valor inválido deveria cair no padrão, veio %d", got)
	}
}

func TestPaths(t *testing.T) {
	dir, ctl, docker := paths()
	if dir == "" || ctl == "" || docker == "" {
		t.Fatal("paths não podem ser vazios")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("diretório de estado deveria existir: %v", err)
	}
}

func TestLockDaemonImpedeSegundoDaemon(t *testing.T) {
	ctl := filepath.Join(t.TempDir(), "nanoc.sock")
	if err := lockDaemon(ctl); err != nil {
		t.Fatalf("primeiro lock deveria passar: %v", err)
	}
	first := daemonLock
	defer first.Close()
	if err := lockDaemon(ctl); err == nil {
		t.Fatal("segundo daemon no mesmo estado deveria ser recusado")
	}
	first.Close()
	if err := lockDaemon(ctl); err != nil {
		t.Fatalf("depois que o primeiro sai, o lock deveria ficar livre: %v", err)
	}
	daemonLock.Close()
}
