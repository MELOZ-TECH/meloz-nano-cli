package main

import (
	"os"
	"testing"
)

func TestEnvOr(t *testing.T) {
	t.Setenv("NANO_TEST_KEY", "")
	if got := envOr("NANO_TEST_KEY", "fallback"); got != "fallback" {
		t.Fatalf("vazio deveria cair no default, veio %q", got)
	}
	t.Setenv("NANO_TEST_KEY", "valor")
	if got := envOr("NANO_TEST_KEY", "fallback"); got != "valor" {
		t.Fatalf("esperava valor, veio %q", got)
	}
}

func TestMemBytes(t *testing.T) {
	t.Setenv("NANO_MEM_MB", "")
	if got := memBytes(); got != 1024*1024*1024 {
		t.Fatalf("default deveria ser 1 GiB, veio %d", got)
	}
	t.Setenv("NANO_MEM_MB", "2048")
	if got := memBytes(); got != 2*1024*1024*1024 {
		t.Fatalf("esperava 2 GiB, veio %d", got)
	}
	t.Setenv("NANO_MEM_MB", "invalido")
	if got := memBytes(); got != 1024*1024*1024 {
		t.Fatalf("valor inválido deveria cair no default, veio %d", got)
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
