//go:build krun

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// runKrunDaemon é o runDaemon do VZ com o libkrun por baixo (NANOC_BACKEND=krun).
// Mesmos sockets, mesmo estado e mesmo protocolo de controle, então up, down,
// status, ports e exec do CLI não mudam. Não há controlador de balão: o free page
// reporting em modo Replace devolve ao macOS o que o guest libera.
func runKrunDaemon(kernel, initrd, _, sockPath, ctlPath string) error {
	if err := lockDaemon(ctlPath); err != nil {
		return err
	}
	if os.Getenv("NANOC_DATA_BLK") == "0" || tcpTransport() {
		return fmt.Errorf("NANOC_DATA_BLK=0 e NANOC_VSOCK=0 não existem no backend libkrun")
	}
	home, _ := os.UserHomeDir()
	stateDir := filepath.Dir(ctlPath)
	logEvent(eventsFile(stateDir), "daemon_start", map[string]any{"backend": "libkrun", "version": version,
		"mem_max_mb": memBytes() / mib, "mtu": krunMTU()})
	modloop := envOr("NANOC_MODLOOP", filepath.Join(filepath.Dir(kernel), "modloop-virt"))
	if _, err := os.Stat(modloop); err != nil {
		// sem modloop o data.img deixaria de ser o vdb que a cmdline aponta
		return fmt.Errorf("backend libkrun precisa do modloop: %w", err)
	}
	data := envOr("NANOC_DATA", filepath.Join(home, ".nanoc", "data.img"))
	dataDir := filepath.Dir(data)
	if v := os.Getenv("NANOC_SHARE_DIR"); v != "" {
		dataDir = v
	}
	_ = os.MkdirAll(dataDir, 0o755)
	dataPath := filepath.Join(dataDir, envOr("NANOC_DATA_REL", filepath.Base(data)))
	created, err := ensureDataImage(dataPath)
	if err != nil {
		return err
	}
	noDocker := os.Getenv("NANOC_NO_DOCKER") == "1"
	cmdline := krunCmdline(created, noDocker)
	if v := os.Getenv("NANOC_HOSTNAME"); v != "" {
		cmdline += " nanoc.host=" + v
	}
	users := ""
	if os.Getenv("NANOC_SHARE_USERS") != "0" {
		if _, err := os.Stat("/Users"); err == nil {
			users = "/Users"
		}
	}

	vn, fd, err := startNetwork()
	if err != nil {
		return fmt.Errorf("rede: %w", err)
	}
	// o libkrun escuta nestes sockets e liga cada conexão à porta vsock do agente
	agentDocker := filepath.Join(stateDir, "krun-2375.sock")
	agentControl := filepath.Join(stateDir, "krun-2376.sock")
	o := krunOpts{
		kernel:      kernel,
		initrd:      envOr("NANOC_KRUN_INITRD", initrd),
		modloop:     modloop,
		data:        dataPath,
		share:       dataDir,
		users:       users,
		sockDocker:  agentDocker,
		sockControl: agentControl,
		cmdline:     cmdline,
		cpus:        2,
		ramMiB:      int(memBytes() >> 20),
		fprMode:     1, // KRUN_FREE_PAGE_REPORTING_MODE_REPLACE
		logLevel:    2,
		netFD:       fd,
		netFeatures: krunNetFeatures(),
	}

	pidPath := ctlPath + ".pid"
	var stopping atomic.Bool
	stop := func(why string) {
		if !stopping.CompareAndSwap(false, true) {
			return
		}
		fmt.Fprintf(os.Stderr, "%s parada: %s\n", time.Now().Format(time.RFC3339), why)
		_ = os.Remove(pidPath)
		// o libkrun encerra o processo quando o guest desliga; dockerd e containerd
		// recebem TERM antes para fechar os bancos dos containers
		go krunAgent(agentControl, map[string]any{"cmd": "run", "argv": []string{"sh", "-c",
			"killall -TERM dockerd containerd 2>/dev/null; sleep 3; sync; poweroff -f"}})
		go func() {
			time.Sleep(20 * time.Second)
			fmt.Fprintln(os.Stderr, "guest não desligou em 20 s; daemon saindo")
			os.Exit(0)
		}()
	}

	// o resto sobe numa goroutine: krunRun prende a thread principal até o fim
	go func() {
		if err := krunServe(o, vn, noDocker, sockPath, ctlPath, pidPath, stop); err != nil {
			fmt.Fprintln(os.Stderr, "erro:", err)
			os.Exit(1)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() { stop(fmt.Sprintf("sinal %v", <-sig)) }()

	fmt.Fprintf(os.Stderr, "%s backend libkrun: %d MiB, free page reporting Replace\n",
		time.Now().Format(time.RFC3339), o.ramMiB)
	return krunRun(o)
}

// krunServe espera o guest e publica o socket do Docker, o de controle e as portas.
func krunServe(o krunOpts, vn interface {
	Dial(network, addr string) (net.Conn, error)
}, noDocker bool, sockPath, ctlPath, pidPath string, stop func(string)) error {
	ev := eventsFile(filepath.Dir(ctlPath))
	started := time.Now()
	if err := krunWaitGuest(o.sockDocker, o.sockControl, noDocker); err != nil {
		logEvent(ev, "guest_timeout", map[string]any{"error": err.Error()})
		return err
	}
	logEvent(ev, "guest_ready", map[string]any{"boot_s": int(time.Since(started).Seconds())})
	if c, err := net.DialTimeout("unix", ctlPath, 300*time.Millisecond); err == nil {
		c.Close()
		return fmt.Errorf("já existe um daemon em %s", ctlPath)
	}
	_ = os.Remove(ctlPath)
	ctl, err := net.Listen("unix", ctlPath)
	if err != nil {
		return err
	}
	_ = os.WriteFile(pidPath, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o644)

	pf := newPortForward()
	pf.dial = func(addr string) (net.Conn, error) { return vn.Dial("tcp", addr) }
	if !noDocker {
		_ = os.Remove(sockPath)
		ln, err := net.Listen("unix", sockPath)
		if err != nil {
			return err
		}
		go func() {
			for {
				hc, err := ln.Accept()
				if err != nil {
					return
				}
				go func(hc net.Conn) {
					defer hc.Close()
					gc, err := net.Dial("unix", o.sockDocker)
					if err != nil {
						return
					}
					defer gc.Close()
					// o half-close precisa do libkrun com o fim da escrita corrigido
					// (shutdown no lugar de reset; ver build/krun/README.md)
					proxyCopy(hc, gc)
				}(hc)
			}
		}()
		go pf.run(sockPath)
		fmt.Fprintf(os.Stderr, "docker do guest disponível em %s\n", sockPath)
	}

	go krunDropCaches(o.sockControl)

	for {
		c, err := ctl.Accept()
		if err != nil {
			return nil
		}
		go func(c net.Conn) {
			defer c.Close()
			var req struct {
				Cmd       string   `json:"cmd"`
				Action    string   `json:"action"`
				Container string   `json:"container"`
				Argv      []string `json:"argv"`
			}
			_ = json.NewDecoder(c).Decode(&req)
			resp := map[string]any{"running": true, "pid": os.Getpid(), "docker_sock": sockPath,
				"ports": pf.active(), "bind": listenAddr(), "backend": "libkrun", "mem_max_mb": o.ramMiB}
			switch req.Cmd {
			case "k8s":
				resp = krunAgent(o.sockControl, map[string]any{"cmd": "k8s-" + req.Action})
			case "balloon":
				resp = map[string]any{"ok": false, "message": "sem balão no backend libkrun: a memória livre volta sozinha"}
			case "run":
				resp = krunAgent(o.sockControl, map[string]any{"cmd": "run", "argv": req.Argv})
			case "exec":
				resp = krunAgent(o.sockControl, map[string]any{"cmd": "exec", "container": req.Container, "argv": req.Argv})
			case "stop":
				logEvent(ev, "stop_request", peerInfo(c))
				resp["stopping"] = true
				stop("pedida pelo socket de controle")
			}
			_ = json.NewEncoder(c).Encode(resp)
		}(c)
	}
}

// krunDropCaches faz o que o vinit do OrbStack faz a cada 45 s: cache de arquivo
// não é memória livre para o guest, então o free page reporting não o devolve ao
// macOS, e a VM ficava com o teto inteiro depois de um docker pull.
func krunDropCaches(agentControl string) {
	for {
		time.Sleep(45 * time.Second)
		krunAgent(agentControl, map[string]any{"cmd": "run", "argv": []string{"sh", "-c",
			"echo 1 > /proc/sys/vm/drop_caches"}})
	}
}

// krunWaitGuest espera o agente responder. O libkrun aceita a conexão no socket do
// host antes de o guest escutar e fecha sem dados, então conectar não basta.
func krunWaitGuest(agentDocker, agentControl string, noDocker bool) error {
	cl := dockerClient(agentDocker)
	for i := 0; i < 120; i++ {
		time.Sleep(time.Second)
		if noDocker {
			if r := krunAgent(agentControl, map[string]any{"cmd": "run", "argv": []string{"true"}}); r["ok"] == true {
				return nil
			}
			continue
		}
		resp, err := cl.Get("http://docker/_ping")
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK && strings.TrimSpace(string(b)) == "OK" {
			return nil
		}
	}
	return fmt.Errorf("guest não respondeu em 120 s")
}

// krunAgent é o guestControlRaw do VZ falando pelo socket que o libkrun publica.
func krunAgent(sock string, payload map[string]any) map[string]any {
	out := map[string]any{"ok": false, "message": "guest sem controle"}
	for i := 0; i < 15; i++ {
		c, err := net.DialTimeout("unix", sock, 5*time.Second)
		if err != nil {
			out["message"] = err.Error()
			time.Sleep(time.Second)
			continue
		}
		_ = json.NewEncoder(c).Encode(payload)
		var resp map[string]any
		err = json.NewDecoder(c).Decode(&resp)
		c.Close()
		if err == nil {
			return resp
		}
		out["message"] = err.Error()
		// EOF sem nenhum byte: o agente ainda não escuta. Outro erro pode vir de um
		// comando que já rodou, e repetir faria ele rodar de novo.
		if err != io.EOF {
			return out
		}
		time.Sleep(time.Second)
	}
	return out
}
