package main

import (
	"crypto/x509"
	"fmt"
	"net"
	"reflect"
	"testing"
	"time"
)

func TestDomainLabel(t *testing.T) {
	for in, want := range map[string]string{
		"/web":           "web",
		"/My_App.1":      "my-app-1",
		"/dbolt-zitadel": "dbolt-zitadel",
		"/__":            "",
	} {
		if got := domainLabel(in); got != want {
			t.Errorf("%q: %q, quero %q", in, got, want)
		}
	}
}

func TestDomainTargets(t *testing.T) {
	body := []byte(`[
	 {"Names":["/web"],"Ports":[{"PrivatePort":443,"PublicPort":8443,"Type":"tcp"},{"PrivatePort":80,"PublicPort":8081,"Type":"tcp"},{"PrivatePort":80,"PublicPort":8081,"Type":"tcp"}]},
	 {"Names":["/api"],"Labels":{"nanoc.port":"9000"},"Ports":[{"PrivatePort":80,"PublicPort":8082,"Type":"tcp"},{"PrivatePort":9000,"PublicPort":19000,"Type":"tcp"}]},
	 {"Names":["/db"],"Ports":[{"PrivatePort":5432,"PublicPort":1111,"Type":"tcp"}]},
	 {"Names":["/dns"],"Ports":[{"PrivatePort":53,"PublicPort":5353,"Type":"udp"}]},
	 {"Names":["/semporta"],"Ports":[{"PrivatePort":80,"Type":"tcp"}]}]`)
	want := map[string]int{"web": 8081, "api": 19000, "db": 1111}
	if got := domainTargets(body); !reflect.DeepEqual(got, want) {
		t.Errorf("%v, quero %v", got, want)
	}
}

func TestLocalCA(t *testing.T) {
	dir := t.TempDir()
	ca, err := loadOrCreateCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := ca.leaf("web.nanoc.local")
	if err != nil {
		t.Fatal(err)
	}
	// a CA gravada é a mesma ao recarregar, e o certificado vale para o nome
	again, err := loadOrCreateCA(dir)
	if err != nil || !again.cert.Equal(ca.cert) {
		t.Fatalf("CA recarregada difere: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(again.cert)
	if _, err := leaf.Leaf.Verify(x509.VerifyOptions{DNSName: "web.nanoc.local", Roots: pool}); err != nil {
		t.Errorf("certificado não verifica: %v", err)
	}
	// a restrição de nome impede usar a CA para outros domínios
	other, err := ca.leaf("example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Leaf.Verify(x509.VerifyOptions{DNSName: "example.com", Roots: pool}); err == nil {
		t.Error("a CA assinou example.com e o certificado verificou")
	}
}

func TestDomainProxyPort(t *testing.T) {
	d := newDomainProxy(nil)
	d.set(map[string]int{"web": 8081})
	for host, want := range map[string]int{"web.nanoc.local": 8081, "WEB.nanoc.local:443": 8081, "web.example.com": 0, "api.nanoc.local": 0} {
		if got, _ := d.port(host); got != want {
			t.Errorf("%q: %d, quero %d", host, got, want)
		}
	}
}

func TestLoopbackOnly(t *testing.T) {
	ln, err := listenLoopback(0)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Write([]byte("ok"))
			c.Close()
		}
	}()
	read := func(addr string) string {
		c, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			return ""
		}
		defer c.Close()
		c.SetReadDeadline(time.Now().Add(2 * time.Second))
		b := make([]byte, 2)
		n, _ := c.Read(b)
		return string(b[:n])
	}
	if got := read(fmt.Sprintf("127.0.0.1:%d", port)); got != "ok" {
		t.Errorf("127.0.0.1: %q", got)
	}
	// pelo IP da rede local, se houver: a conexão é fechada sem resposta
	if addrs, _ := net.InterfaceAddrs(); len(addrs) > 0 {
		for _, a := range addrs {
			if ip, ok := a.(*net.IPNet); ok && ip.IP.To4() != nil && !ip.IP.IsLoopback() {
				if got := read(fmt.Sprintf("%s:%d", ip.IP, port)); got == "ok" {
					t.Errorf("%s respondeu", ip.IP)
				}
				break
			}
		}
	}
}
