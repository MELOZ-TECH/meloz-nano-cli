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

// domainSuffix: cada container com porta publicada vira https://<nome>.nanoc.local
// no Mac. O nome resolve para 127.0.0.1 pelo mDNSResponder (mdns_darwin.go) e um
// proxy HTTPS no loopback repassa para a porta publicada do container.
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

// domainTargets lê GET /containers/json e devolve nome → porta publicada (no
// guest) que o proxy usa para aquele container.
func domainTargets(body []byte) map[string]int {
	var cs []struct {
		Names  []string
		Labels map[string]string
		Ports  []struct {
			PrivatePort int
			PublicPort  int
			Type        string
		}
	}
	if json.Unmarshal(body, &cs) != nil {
		return nil
	}
	out := map[string]int{}
	for _, c := range cs {
		if len(c.Names) == 0 {
			continue
		}
		name := domainLabel(c.Names[0])
		pub := map[int]int{} // porta do container → porta publicada
		for _, p := range c.Ports {
			if p.Type == "tcp" && p.PublicPort > 0 {
				if old, ok := pub[p.PrivatePort]; !ok || p.PublicPort < old {
					pub[p.PrivatePort] = p.PublicPort
				}
			}
		}
		if name == "" || len(pub) == 0 {
			continue
		}
		port := 0
		if v, err := strconv.Atoi(c.Labels["nanoc.port"]); err == nil {
			port = pub[v]
		}
		for _, w := range webPorts {
			if port == 0 {
				port = pub[w]
			}
		}
		if port == 0 {
			priv := make([]int, 0, len(pub))
			for p := range pub {
				priv = append(priv, p)
			}
			sort.Ints(priv)
			port = pub[priv[0]]
		}
		out[name] = port
	}
	return out
}

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
	targets map[string]int
	dial    func(addr string) (net.Conn, error)
	proxy   *httputil.ReverseProxy
}

type portKey struct{}

func newDomainProxy(dial func(addr string) (net.Conn, error)) *domainProxy {
	d := &domainProxy{targets: map[string]int{}, dial: dial}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dial(fmt.Sprintf("%s:%d", guestIP(), ctx.Value(portKey{}).(int)))
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

func (d *domainProxy) set(t map[string]int) {
	d.mu.Lock()
	d.targets = t
	d.mu.Unlock()
}

func (d *domainProxy) port(host string) (int, bool) {
	h := strings.ToLower(host)
	if i := strings.LastIndexByte(h, ':'); i >= 0 && !strings.Contains(h[i:], "]") {
		h = h[:i]
	}
	if !strings.HasSuffix(h, domainSuffix) {
		return 0, false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	p, ok := d.targets[strings.TrimSuffix(h, domainSuffix)]
	return p, ok
}

func (d *domainProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	port, ok := d.port(r.Host)
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
		fmt.Fprintf(w, "nanoc: nenhum container com porta publicada se chama %q\n\n%s\n", r.Host, strings.Join(names, "\n"))
		return
	}
	d.proxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), portKey{}, port)))
}

// listenLoopback escuta em todas as interfaces, que é o que o macOS deixa um
// usuário comum fazer nas portas abaixo de 1024, mas presa ao lo0 (IP_BOUND_IF):
// a porta não aparece para a rede local.
func listenLoopback(port int) (net.Listener, error) {
	lc := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		var serr error
		err := c.Control(func(fd uintptr) {
			lo, err := net.InterfaceByName("lo0")
			if err != nil {
				serr = err
				return
			}
			serr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_BOUND_IF, lo.Index)
		})
		if err != nil {
			return err
		}
		return serr
	}}
	ln, err := lc.Listen(context.Background(), "tcp4", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		return nil, err
	}
	return loopbackOnly{ln}, nil
}

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
func runDomains(dockerSock, stateDir, ev string, dial func(addr string) (net.Conn, error)) {
	if os.Getenv("NANOC_DOMAINS") == "0" {
		return
	}
	ca, err := loadOrCreateCA(stateDir)
	if err != nil {
		logEvent(ev, "domains_off", map[string]any{"error": err.Error()})
		return
	}
	d := newDomainProxy(dial)
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
