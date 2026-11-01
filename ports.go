package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"time"
)

// portForward publica em 127.0.0.1 as portas que o Docker publica no guest.
// Assim `docker run -p 8080:80` fica acessível em http://localhost:8080 no Mac.
type portForward struct {
	mu        sync.Mutex
	listeners map[int]net.Listener
	client    *http.Client
}

func newPortForward() *portForward {
	return &portForward{listeners: map[int]net.Listener{}}
}

// dockerClient: um só cliente para todas as consultas. Um Transport novo por
// consulta deixava uma conexão keep-alive aberta a cada 2 s (sem timeout), e
// cada uma prendia descritores no agente do guest.
func dockerClient(sock string) *http.Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
		MaxIdleConns:    1,
		IdleConnTimeout: 30 * time.Second,
	}
	return &http.Client{Transport: tr, Timeout: 5 * time.Second}
}

func (p *portForward) run(sock string) {
	for {
		p.sync(sock)
		time.Sleep(2 * time.Second)
	}
}

// listenAddr: 127.0.0.1 por padrão; 0.0.0.0 quando NANOC_LAN=1.
func listenAddr() string {
	if os.Getenv("NANOC_LAN") == "1" {
		return "0.0.0.0"
	}
	return "127.0.0.1"
}

func (p *portForward) sync(sock string) {
	if p.client == nil {
		p.client = dockerClient(sock)
	}
	resp, err := p.client.Get("http://docker/containers/json")
	if err != nil {
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return
	}
	var containers []struct {
		Ports []struct {
			PublicPort int `json:"PublicPort"`
		} `json:"Ports"`
	}
	if json.Unmarshal(body, &containers) != nil {
		return
	}
	want := map[int]bool{}
	for _, c := range containers {
		for _, pt := range c.Ports {
			if pt.PublicPort > 0 {
				want[pt.PublicPort] = true
			}
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for port, ln := range p.listeners {
		if !want[port] {
			ln.Close()
			delete(p.listeners, port)
		}
	}
	for port := range want {
		if _, ok := p.listeners[port]; ok {
			continue
		}
		ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", listenAddr(), port))
		if err != nil {
			continue
		}
		p.listeners[port] = ln
		go p.serve(ln, port)
	}
}

func (p *portForward) serve(ln net.Listener, port int) {
	for {
		hc, err := ln.Accept()
		if err != nil {
			return
		}
		go func(hc net.Conn) {
			defer hc.Close()
			gc, err := net.DialTimeout("tcp", fmt.Sprintf("192.168.64.2:%d", port), 5*time.Second)
			if err != nil {
				return
			}
			defer gc.Close()
			go io.Copy(gc, hc)
			io.Copy(hc, gc)
		}(hc)
	}
}

func (p *portForward) active() []int {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]int, 0, len(p.listeners))
	for port := range p.listeners {
		out = append(out, port)
	}
	return out
}
