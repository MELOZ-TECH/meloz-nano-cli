package main

import "testing"

func TestNextTarget(t *testing.T) {
	const floor, ceil = 1024, 4096
	livre := func(mb uint64) guestMem { return guestMem{TotalKB: ceil * 1024, AvailKB: mb * 1024} }
	cases := []struct {
		nome     string
		cur      uint64
		g        guestMem
		critico  bool
		idle     int
		esperado uint64
	}{
		{"pouca folga cresce 512", 1024, livre(100), false, 0, 1536},
		{"mac em pressão cresce 256", 1024, livre(100), true, 0, 1280},
		{"pressão PSI cresce", 2048, guestMem{TotalKB: 1, AvailKB: 1500 * 1024, PSI10: 25}, false, 0, 2560},
		{"não passa do teto", 3900, livre(50), false, 0, ceil},
		{"folga sem tempo mantém", 2048, livre(1800), false, 5, 2048},
		{"folga longa encolhe 256", 2048, livre(1800), false, 30, 1792},
		{"não desce do piso", 1100, livre(900), false, 30, floor},
		{"uso normal mantém", 2048, livre(600), false, 30, 2048},
	}
	for _, c := range cases {
		if got := nextTarget(c.cur, floor, ceil, c.g, c.critico, c.idle); got != c.esperado {
			t.Errorf("%s: nextTarget(%d) = %d; esperava %d", c.nome, c.cur, got, c.esperado)
		}
	}
}

func TestMemLimites(t *testing.T) {
	t.Setenv("NANOC_MEM_MB", "3072")
	t.Setenv("NANOC_MEM_MIN_MB", "8192")
	if got := memBytes() / mib; got != 3072 {
		t.Fatalf("teto = %d; esperava 3072", got)
	}
	if got := memFloorMB(); got != 3072 {
		t.Fatalf("piso acima do teto deveria cair para o teto; deu %d", got)
	}
	t.Setenv("NANOC_MEM_MB", "")
	if got := memBytes() / mib; got < 2048 || got > 8192 {
		t.Fatalf("teto padrão fora de 2–8 GB: %d", got)
	}
}
