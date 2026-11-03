package main

import (
	"bufio"
	"crypto/x509"
	"fmt"
	"io"
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
	 {"Names":["/web"],"Ports":[{"PrivatePort":443,"PublicPort":8443,"Type":"tcp"},{"PrivatePort":80,"PublicPort":8081,"Type":"tcp"},{"PrivatePort":80,"PublicPort":8081,"Type":"tcp"}],
	  "NetworkSettings":{"Networks":{"bridge":{"IPAddress":"172.17.0.2"}}}},
	 {"Names":["/api"],"Labels":{"nanoc.port":"9000"},"Ports":[{"PrivatePort":80,"PublicPort":8082,"Type":"tcp"}],
	  "NetworkSettings":{"Networks":{"app_default":{"IPAddress":"172.18.0.3"}}}},
	 {"Names":["/semporta"],"Ports":[{"PrivatePort":3000,"Type":"tcp"}],
	  "NetworkSettings":{"Networks":{"bridge":{"IPAddress":"172.17.0.4"}}}},
	 {"Names":["/db"],"Ports":[{"PrivatePort":5432,"PublicPort":1111,"Type":"tcp"}]},
	 {"Names":["/dns"],"Ports":[{"PrivatePort":53,"Type":"udp"}],"NetworkSettings":{"Networks":{"bridge":{"IPAddress":"172.17.0.5"}}}},
	 {"Names":["/nada"],"NetworkSettings":{"Networks":{"bridge":{"IPAddress":"172.17.0.6"}}}}]`)
	want := map[string]domainTarget{
		"web":      {IP: "172.17.0.2", Port: 80, Pub: 8081},
		"api":      {IP: "172.18.0.3", Port: 9000},
		"semporta": {IP: "172.17.0.4", Port: 3000},
		"db":       {Port: 5432, Pub: 1111},
	}
	if got := domainTargets(body); !reflect.DeepEqual(got, want) {
		t.Errorf("%v, quero %v", got, want)
	}
}

func TestRelayDialer(t *testing.T) {
	agent := func(ack byte) func() (net.Conn, error) {
		return func() (net.Conn, error) {
			mac, guest := net.Pipe()
			go func() {
				addr, _ := bufio.NewReader(guest).ReadString('\n')
				if addr == "172.17.0.2:80\n" {
					guest.Write([]byte{ack, 'o', 'k'})
				}
				guest.Close()
			}()
			return mac, nil
		}
	}
	c, err := relayDialer(agent(relayOK))("172.17.0.2:80")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(c)
	if string(b) != "ok" {
		t.Errorf("depois da confirmação veio %q", b)
	}
	if _, err := relayDialer(agent(0))("172.17.0.2:80"); err == nil {
		t.Error("sem a confirmação o repasse devia falhar")
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
	d := newDomainProxy(nil, nil)
	d.set(map[string]domainTarget{"web": {Pub: 8081}})
	for host, want := range map[string]int{"web.nanoc.local": 8081, "WEB.nanoc.local:443": 8081, "web.example.com": 0, "api.nanoc.local": 0} {
		if got, _ := d.target(host); got.Pub != want {
			t.Errorf("%q: %d, quero %d", host, got.Pub, want)
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
	for _, local := range []string{"127.0.0.1", "[::1]"} {
		if got := read(fmt.Sprintf("%s:%d", local, port)); got != "ok" {
			t.Errorf("%s: %q", local, got)
		}
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
