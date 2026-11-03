package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

// runWatch: nanoc watch <args do daemon>. Sobe o daemon como filho e espera por
// ele. Um processo não registra o próprio SIGKILL, mas o pai registra: o sinal, a
// hora e uma foto do Mac vão para events.log. O nome "watch" não casa com
// `pkill -f "nanoc daemon"` de propósito: se os dois somem juntos, alguém matou
// pelo nome.
func runWatch(args []string) error {
	if len(args) < 5 {
		return fmt.Errorf("uso: nanoc watch <kernel> <initrd> <share> <docker.sock> <ctl.sock>")
	}
	ev := eventsFile(filepath.Dir(args[4]))
	self, err := os.Executable()
	if err != nil {
		return err
	}
	c := exec.Command(self, append([]string{"daemon"}, args...)...)
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	if err := c.Start(); err != nil {
		return err
	}
	start := time.Now()
	logEvent(ev, "watch_start", map[string]any{"daemon_pid": c.Process.Pid, "version": version,
		"backend": envOr("NANOC_BACKEND", "vz")})

	// sinal no vigia vai para o daemon, que faz a parada normal
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	go func() {
		for s := range sig {
			logEvent(ev, "watch_signal", map[string]any{"signal": s.String()})
			_ = c.Process.Signal(s)
		}
	}()

	_ = c.Wait()
	st := c.ProcessState
	rec := map[string]any{"daemon_pid": c.Process.Pid, "exit_code": st.ExitCode(),
		"uptime_s": int(time.Since(start).Seconds())}
	abnormal := st.ExitCode() != 0
	if ws, ok := st.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		rec["signal"] = ws.Signal().String()
		abnormal = true
	}
	if abnormal {
		rec["host"] = hostSnapshot()
	}
	logEvent(ev, "daemon_exit", rec)
	fmt.Fprintf(os.Stderr, "%s vigia: daemon %d saiu (código %d, sinal %v); detalhes em %s\n",
		time.Now().Format(time.RFC3339), c.Process.Pid, st.ExitCode(), rec["signal"], ev)
	return nil
}
