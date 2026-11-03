package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Eventos do daemon em <estado>/events.log, um JSON por linha, para depurar sem
// chutar: quem pediu a parada, que sinal chegou, como o daemon morreu e como
// estava o Mac nessa hora. O vigia e o daemon escrevem no mesmo arquivo; linhas
// curtas com O_APPEND não se misturam.

const eventsMax = 1 << 20 // passou disso, o arquivo vira events.log.1

func eventsFile(stateDir string) string { return filepath.Join(stateDir, "events.log") }

func logEvent(path, ev string, kv map[string]any) {
	rec := map[string]any{"t": time.Now().Format("2006-01-02T15:04:05.000-07:00"), "ev": ev, "pid": os.Getpid()}
	for k, v := range kv {
		rec[k] = v
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	if st, err := os.Stat(path); err == nil && st.Size() > eventsMax {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	_, _ = f.Write(append(b, '\n'))
	f.Close()
}

// readEvents devolve os últimos n eventos, só os do tipo ev quando ele não é vazio.
func readEvents(path string, n int, ev string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if ev != "" && !strings.Contains(line, `"ev":"`+ev+`"`) {
			continue
		}
		out = append(out, line)
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

// peerInfo diz qual processo está do outro lado de um socket unix (LOCAL_PEERPID)
// e quem é o pai dele: é assim que se sabe quem mandou o daemon parar.
func peerInfo(c net.Conn) map[string]any {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return nil
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return nil
	}
	pid := 0
	_ = raw.Control(func(fd uintptr) {
		pid, _ = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	})
	if pid <= 0 {
		return nil
	}
	info := map[string]any{"peer_pid": pid, "peer_cmd": procField(pid, "command")}
	if ppid, err := strconv.Atoi(procField(pid, "ppid")); err == nil && ppid > 0 {
		info["peer_ppid"] = ppid
		info["peer_parent_cmd"] = procField(ppid, "command")
	}
	return info
}

func procField(pid int, field string) string {
	out, err := exec.Command("ps", "-o", field+"=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	return clip(strings.TrimSpace(string(out)), 200)
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// hostSnapshot é a foto do Mac na hora de um problema: memória, swap, os maiores
// processos e os que nasceram nos últimos 2 minutos (um `kill` costuma vir de um
// deles).
func hostSnapshot() map[string]any {
	snap := map[string]any{}
	if out, err := exec.Command("sysctl", "-n", "vm.swapusage").Output(); err == nil {
		snap["swap"] = strings.TrimSpace(string(out))
	}
	if out, err := exec.Command("memory_pressure").Output(); err == nil {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		snap["memory"] = strings.TrimSpace(lines[len(lines)-1])
	}
	out, err := exec.Command("ps", "-Ao", "etime=,pid=,ppid=,rss=,command=").Output()
	if err != nil {
		return snap
	}
	type proc struct {
		rss  int
		line string
	}
	var recent []string
	var procs []proc
	for _, l := range strings.Split(string(out), "\n") {
		f := strings.Fields(l)
		if len(f) < 5 {
			continue
		}
		rss, _ := strconv.Atoi(f[3])
		line := clip(fmt.Sprintf("pid=%s ppid=%s %dMB %s", f[1], f[2], rss/1024, strings.Join(f[4:], " ")), 200)
		procs = append(procs, proc{rss, line})
		// etime sem hora nem dia é MM:SS
		if mm, _, ok := strings.Cut(f[0], ":"); ok && !strings.ContainsAny(f[0], "-") && strings.Count(f[0], ":") == 1 {
			if m, err := strconv.Atoi(mm); err == nil && m < 2 {
				recent = append(recent, f[0]+" "+line)
			}
		}
	}
	sort.Slice(procs, func(i, j int) bool { return procs[i].rss > procs[j].rss })
	var top []string
	for i := 0; i < len(procs) && i < 8; i++ {
		top = append(top, procs[i].line)
	}
	if len(recent) > 30 {
		recent = recent[len(recent)-30:]
	}
	snap["top_rss"] = top
	snap["recent"] = recent
	return snap
}
