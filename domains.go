package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// domainSuffix: cada container com porta exposta vira https://<nome>.nanoc.local
// no Mac. O nome resolve para 127.0.0.1 pelo mDNSResponder (mdns_darwin.go) e um
// proxy HTTPS no loopback chega ao container pelo repasse do agente no guest.
const domainSuffix = ".nanoc.local"

var dnsLabel = regexp.MustCompile(`[^a-z0-9-]+`)

// domainLabel transforma o nome do container num rótulo DNS: minúsculas, letras,
// números e hífen. Vazio quando não sobra nada.
func domainLabel(name string) string {
	l := dnsLabel.ReplaceAllString(strings.ToLower(strings.TrimPrefix(name, "/")), "-")
	l = strings.Trim(l, "-")
	if len(l) > 63 {
		l = strings.Trim(l[:63], "-")
	}
	return l
}

// webPorts: portas do container tentadas, nesta ordem, quando ele publica mais de
// uma. O rótulo nanoc.port escolhe outra.
var webPorts = []int{80, 8080, 3000, 8000, 5000, 443, 8443}

// domainTarget: para onde vai https://<nome>.nanoc.local. Pelo repasse do agente,
// o IP do container na rede do Docker e a porta dele; a porta publicada, quando
// existe, é a reserva para um agente antigo, sem o repasse.
type domainTarget struct {
	IP   string
	Port int
	Pub  int
}

// domainTargets lê GET /containers/json e escolhe a porta de cada container:
// a do rótulo nanoc.port, senão a primeira de webPorts que ele expõe, senão a
// menor exposta. Containers sem porta exposta nem rótulo ficam de fora.
func domainTargets(body []byte) map[string]domainTarget {
	var cs []struct {
		Names  []string
		Labels map[string]string
		Ports  []struct {
			PrivatePort int
			PublicPort  int
			Type        string
		}
		NetworkSettings struct {
			Networks map[string]struct{ IPAddress string }
		}
	}
	if json.Unmarshal(body, &cs) != nil {
		return nil
	}
	out := map[string]domainTarget{}
	for _, c := range cs {
		if len(c.Names) == 0 {
			continue
		}
		name := domainLabel(c.Names[0])
		ip := ""
		nets := make([]string, 0, len(c.NetworkSettings.Networks))
		for n := range c.NetworkSettings.Networks {
			nets = append(nets, n)
		}
		sort.Strings(nets)
		for _, n := range nets {
			if a := c.NetworkSettings.Networks[n].IPAddress; a != "" {
				ip = a
				break
			}
		}
		exposed := map[int]int{} // porta do container → publicada (0 se não)
		for _, p := range c.Ports {
			if p.Type != "tcp" {
				continue
			}
			if old, ok := exposed[p.PrivatePort]; !ok || (p.PublicPort > 0 && (old == 0 || p.PublicPort < old)) {
				exposed[p.PrivatePort] = p.PublicPort
			}
		}
		port := 0
		if v, err := strconv.Atoi(c.Labels["nanoc.port"]); err == nil && v > 0 && v < 65536 {
			port = v
		}
		for _, w := range webPorts {
			if _, ok := exposed[w]; ok && port == 0 {
				port = w
			}
		}
		if port == 0 && len(exposed) > 0 {
			priv := make([]int, 0, len(exposed))
			for p := range exposed {
				priv = append(priv, p)
			}
			sort.Ints(priv)
			port = priv[0]
		}
		t := domainTarget{IP: ip, Port: port, Pub: exposed[port]}
		if name == "" || port == 0 || (ip == "" && t.Pub == 0) {
			continue
		}
		out[name] = t
	}
	return out
}

// relayDialer conecta no container pelo repasse do agente (porta vsock 2377):
// manda "ip:porta\n" e espera o byte de confirmação.
func relayDialer(open func() (net.Conn, error)) func(addr string) (net.Conn, error) {
	return func(addr string) (net.Conn, error) {
		c, err := open()
		if err != nil {
			return nil, err
		}
		_ = c.SetDeadline(time.Now().Add(10 * time.Second))
		ack := make([]byte, 1)
		if _, err := io.WriteString(c, addr+"\n"); err != nil {
			c.Close()
			return nil, err
		}
		if _, err := io.ReadFull(c, ack); err != nil || ack[0] != relayOK {
			c.Close()
			return nil, fmt.Errorf("repasse do agente para %s recusado", addr)
		}
		_ = c.SetDeadline(time.Time{})
		return c, nil
	}
}

const relayOK = 0x06

// localCA é a autoridade que assina os certificados *.nanoc.local. Fica em
// ~/.nanoc/ca.pem; `nanoc trust` a coloca no Keychain para o navegador aceitar.
type localCA struct {
	cert  *x509.Certificate
	key   *ecdsa.PrivateKey
	mu    sync.Mutex
	leafs map[string]*tls.Certificate
}

func loadOrCreateCA(dir string) (*localCA, error) {
	certPath, keyPath := filepath.Join(dir, "ca.pem"), filepath.Join(dir, "ca-key.pem")
	if cb, err := os.ReadFile(certPath); err == nil {
		kb, err := os.ReadFile(keyPath)
		if err != nil {
			return nil, err
		}
		pair, err := tls.X509KeyPair(cb, kb)
		if err != nil {
			return nil, err
		}
		cert, err := x509.ParseCertificate(pair.Certificate[0])
		if err != nil {
			return nil, err
		}
		key, ok := pair.PrivateKey.(*ecdsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("chave da CA não é ECDSA")
		}
		return &localCA{cert: cert, key: key, leafs: map[string]*tls.Certificate{}}, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	host, _ := os.Hostname()
	tmpl := &x509.Certificate{
		SerialNumber:          randSerial(),
		Subject:               pkix.Name{CommonName: "nanoc local CA (" + host + ")", Organization: []string{"nano-container"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
		// só vale para *.nanoc.local, mesmo se a chave vazar
		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         []string{strings.TrimPrefix(domainSuffix, ".")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	kder, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}), 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &localCA{cert: cert, key: key, leafs: map[string]*tls.Certificate{}}, nil
}

func randSerial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	return n
}

// leaf devolve (e guarda) o certificado de um nome *.nanoc.local.
func (ca *localCA) leaf(host string) (*tls.Certificate, error) {
	ca.mu.Lock()
	defer ca.mu.Unlock()
	if c, ok := ca.leafs[host]; ok && time.Until(c.Leaf.NotAfter) > 24*time.Hour {
		return c, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: randSerial(),
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host},
		NotBefore:    time.Now().Add(-time.Hour),
		// abaixo do limite de 398 dias que os navegadores aceitam
		NotAfter:    time.Now().AddDate(0, 0, 390),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	c := &tls.Certificate{Certificate: [][]byte{der, ca.cert.Raw}, PrivateKey: key, Leaf: leaf}
	ca.leafs[host] = c
	return c, nil
}

// domainProxy repassa https://<nome>.nanoc.local para a porta publicada do
// container, abrindo a conexão no guest pelo mesmo caminho do repasse de portas.
type domainProxy struct {
	mu      sync.Mutex
	targets map[string]domainTarget
	proxy   *httputil.ReverseProxy
}

type targetKey struct{}

// newDomainProxy: relay alcança o container pelo agente; dial abre a porta
// publicada no guest, a reserva quando o agente não tem o repasse.
func newDomainProxy(dial, relay func(addr string) (net.Conn, error)) *domainProxy {
	d := &domainProxy{targets: map[string]domainTarget{}}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			t := ctx.Value(targetKey{}).(domainTarget)
			if t.IP != "" && relay != nil {
				c, err := relay(fmt.Sprintf("%s:%d", t.IP, t.Port))
				if err == nil || t.Pub == 0 {
					return c, err
				}
			}
			return dial(fmt.Sprintf("%s:%d", guestIP(), t.Pub))
		},
		// uma porta por container: sem reaproveitar conexões entre containers
		DisableKeepAlives: true,
	}
	d.proxy = &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.URL.Scheme = "http"
			r.Out.URL.Host = r.In.Host
			r.Out.Host = r.In.Host
			r.SetXForwarded()
		},
		Transport: tr,
	}
	return d
}

func (d *domainProxy) set(t map[string]domainTarget) {
	d.mu.Lock()
	d.targets = t
	d.mu.Unlock()
}

func (d *domainProxy) target(host string) (domainTarget, bool) {
	h := strings.ToLower(host)
	if i := strings.LastIndexByte(h, ':'); i >= 0 && !strings.Contains(h[i:], "]") {
		h = h[:i]
	}
	if !strings.HasSuffix(h, domainSuffix) {
		return domainTarget{}, false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	t, ok := d.targets[strings.TrimSuffix(h, domainSuffix)]
	return t, ok
}

func (d *domainProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	t, ok := d.target(r.Host)
	if !ok {
		d.mu.Lock()
		names := make([]string, 0, len(d.targets))
		for n := range d.targets {
			names = append(names, "https://"+n+domainSuffix)
		}
		d.mu.Unlock()
		sort.Strings(names)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, "nanoc: nenhum container com porta exposta se chama %q\n\n%s\n", r.Host, strings.Join(names, "\n"))
		return
	}
	d.proxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), targetKey{}, t)))
}

// listenLoopback escuta em todas as interfaces, que é o que o macOS deixa um
// usuário comum fazer nas portas abaixo de 1024, mas presa ao lo0 (IP_BOUND_IF):
// a porta não aparece para a rede local. Escuta em IPv4 e IPv6, porque os nomes
// resolvem para 127.0.0.1 e ::1.
func listenLoopback(port int) (net.Listener, error) {
	lo, err := net.InterfaceByName("lo0")
	if err != nil {
		return nil, err
	}
	bound := func(level, opt int) net.ListenConfig {
		return net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
			var serr error
			if err := c.Control(func(fd uintptr) { serr = unix.SetsockoptInt(int(fd), level, opt, lo.Index) }); err != nil {
				return err
			}
			return serr
		}}
	}
	lc4 := bound(unix.IPPROTO_IP, unix.IP_BOUND_IF)
	ln4, err := lc4.Listen(context.Background(), "tcp4", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		return nil, err
	}
	// a porta 0 dos testes vira a que o IPv4 recebeu
	port = ln4.Addr().(*net.TCPAddr).Port
	lc6 := bound(unix.IPPROTO_IPV6, unix.IPV6_BOUND_IF)
	ln6, err := lc6.Listen(context.Background(), "tcp6", fmt.Sprintf("[::]:%d", port))
	if err != nil {
		// sem IPv6 o cliente cai para 127.0.0.1
		return loopbackOnly{ln4}, nil
	}
	return loopbackOnly{newMultiListener(ln4, ln6)}, nil
}

// multiListener junta os listeners de IPv4 e IPv6 num só Accept.
type multiListener struct {
	lns   []net.Listener
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func newMultiListener(lns ...net.Listener) *multiListener {
	m := &multiListener{lns: lns, conns: make(chan net.Conn), done: make(chan struct{})}
	for _, ln := range lns {
		go func(ln net.Listener) {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				select {
				case m.conns <- c:
				case <-m.done:
					c.Close()
					return
				}
			}
		}(ln)
	}
	return m
}

func (m *multiListener) Accept() (net.Conn, error) {
	select {
	case c := <-m.conns:
		return c, nil
	case <-m.done:
		return nil, net.ErrClosed
	}
}

func (m *multiListener) Close() error {
	m.once.Do(func() {
		close(m.done)
		for _, ln := range m.lns {
			ln.Close()
		}
	})
	return nil
}

func (m *multiListener) Addr() net.Addr { return m.lns[0].Addr() }

// loopbackOnly fecha toda conexão que não venha de 127.0.0.0/8. Uma conexão do
// próprio Mac para o IP dele na rede local chega pelo lo0 e passaria pelo
// IP_BOUND_IF; a origem diz se ela é mesmo local.
type loopbackOnly struct{ net.Listener }

func (l loopbackOnly) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if a, ok := c.RemoteAddr().(*net.TCPAddr); ok && a.IP.IsLoopback() {
			return c, nil
		}
		c.Close()
	}
}

// runDomains liga os nomes *.nanoc.local, o proxy HTTPS (NANOC_HTTPS_PORT, 443) e
// o HTTP (NANOC_HTTP_PORT, 80; desiste se a porta estiver ocupada, como pelo
// Apache do macOS). NANOC_DOMAINS=0 desliga.
func runDomains(dockerSock, stateDir, ev string, dial, relay func(addr string) (net.Conn, error)) {
	if os.Getenv("NANOC_DOMAINS") == "0" {
		return
	}
	ca, err := loadOrCreateCA(stateDir)
	if err != nil {
		logEvent(ev, "domains_off", map[string]any{"error": err.Error()})
		return
	}
	d := newDomainProxy(dial, relay)
	httpsPort := envInt("NANOC_HTTPS_PORT", 443)
	if ln, err := listenLoopback(httpsPort); err == nil {
		srv := &http.Server{Handler: d, ReadHeaderTimeout: 30 * time.Second, TLSConfig: &tls.Config{
			GetCertificate: func(h *tls.ClientHelloInfo) (*tls.Certificate, error) {
				name := strings.ToLower(h.ServerName)
				if !strings.HasSuffix(name, domainSuffix) {
					return nil, fmt.Errorf("nanoc: %q não é %s", name, domainSuffix)
				}
				return ca.leaf(name)
			},
		}}
		go srv.ServeTLS(ln, "", "")
	} else {
		logEvent(ev, "domains_https_off", map[string]any{"port": httpsPort, "error": err.Error()})
	}
	httpPort := envInt("NANOC_HTTP_PORT", 80)
	if ln, err := listenLoopback(httpPort); err == nil {
		go (&http.Server{Handler: d, ReadHeaderTimeout: 30 * time.Second}).Serve(ln)
	} else {
		logEvent(ev, "domains_http_off", map[string]any{"port": httpPort, "error": err.Error()})
	}

	names := newLocalNames()
	cl := dockerClient(dockerSock)
	var cur []string
	for {
		if resp, err := cl.Get("http://docker/containers/json"); err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			t := domainTargets(b)
			d.set(t)
			want := make([]string, 0, len(t))
			for n := range t {
				want = append(want, n+domainSuffix)
			}
			sort.Strings(want)
			if strings.Join(want, ",") != strings.Join(cur, ",") {
				names.set(want)
				cur = want
				logEvent(ev, "domains", map[string]any{"names": want})
			}
		}
		time.Sleep(2 * time.Second)
	}
}

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 && v < 65536 {
		return v
	}
	return def
}
