package main

import (
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"
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

const relayOK = 0x06

// proxyContainer: o Mac manda "ip:porta\n" e o agente conecta no container de
// dentro da VM, onde as redes do Docker são alcançáveis. Só IPv4 privado (as
// redes do Docker), para o repasse não virar um proxy para qualquer lugar.
func proxyContainer(c io.ReadWriteCloser) {
	addr, err := readAddr(c)
	if err != nil {
		c.Close()
		return
	}
	up, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		c.Close()
		return
	}
	// um byte de confirmação: o Mac sabe que a conexão abriu (um agente antigo,
	// sem esta porta, só fecha) e pode cair para a porta publicada
	if _, err := c.Write([]byte{relayOK}); err != nil {
		up.Close()
		c.Close()
		return
	}
	relay(c, up)
}

// readAddr lê "ip:porta\n" byte a byte, para não consumir o que vem depois.
func readAddr(r io.Reader) (string, error) {
	var b []byte
	one := make([]byte, 1)
	for len(b) < 64 {
		if _, err := io.ReadFull(r, one); err != nil {
			return "", err
		}
		if one[0] == '\n' {
			return checkAddr(string(b))
		}
		b = append(b, one[0])
	}
	return "", fmt.Errorf("endereço longo demais")
}

func checkAddr(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", err
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.To4() == nil || !ip.IsPrivate() {
		return "", fmt.Errorf("%s não é IPv4 privado", host)
	}
	if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
		return "", fmt.Errorf("porta inválida: %s", port)
	}
	return addr, nil
}
