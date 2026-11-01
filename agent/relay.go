package main

import (
	"io"
	"sync"
)

// halfCloser é uma conexão que sabe fechar só o sentido de escrita.
type halfCloser interface {
	CloseWrite() error
}

// relay encaminha nos dois sentidos e fecha as duas pontas no fim.
// Quando um lado chega ao EOF, o outro recebe CloseWrite: assim o dockerd vê o
// fim da requisição e fecha a conexão dele, em vez de ficar com o FD aberto
// para sempre (era o vazamento que esgotava os descritores do agente).
func relay(a, b io.ReadWriteCloser) {
	var wg sync.WaitGroup
	wg.Add(2)
	pipe := func(dst, src io.ReadWriteCloser) {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		if hc, ok := dst.(halfCloser); ok {
			if hc.CloseWrite() == nil {
				return
			}
		}
		// sem half-close: fechar tudo é a única forma de liberar o outro sentido
		_ = dst.Close()
		_ = src.Close()
	}
	go pipe(b, a)
	go pipe(a, b)
	wg.Wait()
	_ = a.Close()
	_ = b.Close()
}
