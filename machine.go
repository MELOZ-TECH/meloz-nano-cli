package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"syscall"
	"time"
)

type machineConfig struct {
	Name string `json:"name"`
	IP   string `json:"ip"`
	Cpus int    `json:"cpus"`
	Mem  int    `json:"mem_mb"`
}

func machinesDir() string {
	dir, _, _ := paths()
	d := filepath.Join(dir, "machines")
	_ = os.MkdirAll(d, 0o755)
	return d
}

func machinePath(name string) string { return filepath.Join(machinesDir(), name) }

func loadMachine(name string) (*machineConfig, error) {
	b, err := os.ReadFile(filepath.Join(machinePath(name), "config.json"))
	if err != nil {
		return nil, fmt.Errorf("máquina %q não existe", name)
	}
	var m machineConfig
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func machineCtl(name string) string  { return filepath.Join(machinePath(name), "ctl.sock") }
func machineLog(name string) string  { return filepath.Join(machinePath(name), "daemon.log") }
func machineData(name string) string { return filepath.Join(machinePath(name), "data.img") }

func machineRunning(name string) bool {
	c, err := net.DialTimeout("unix", machineCtl(name), 300*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

func machineCmd(args []string) error {
	if len(args) == 0 {
		fmt.Println(`uso: nanoc machine <create|ls|start|stop|rm|exec|status> [nome] [-- cmd]`)
		return nil
	}
	action, name := args[0], ""
	if len(args) > 1 {
		name = args[1]
	}
	switch action {
	case "create":
		if name == "" {
			return fmt.Errorf("uso: nanoc machine create <nome>")
		}
		return machineCreate(name)
	case "ls":
		return machineList()
	case "start":
		return machineStart(name)
	case "stop":
		return machineStop(name)
	case "rm":
		return machineRemove(name)
	case "status":
		if machineRunning(name) {
			fmt.Printf("%s: rodando\n", name)
		} else {
			fmt.Printf("%s: parada\n", name)
		}
		return nil
	case "ssh":
		return machineSSH(name, args[2:])
	case "exec":
		if len(args) < 3 {
			return fmt.Errorf("uso: nanoc machine exec <nome> -- <cmd...>")
		}
		argv := args[2:]
		if argv[0] == "--" {
			argv = argv[1:]
		}
		return machineExec(name, argv)
	}
	return fmt.Errorf("ação desconhecida: %s", action)
}

func machineCreate(name string) error {
	dir := machinePath(name)
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("máquina %q já existe", name)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// IP fixo a partir de .10 para não colidir com a VM do Docker (.2)
	n := len(machineNames())
	cfg := machineConfig{Name: name, IP: fmt.Sprintf("192.168.64.%d", 10+n), Cpus: 2, Mem: 1024}
	if err := ensureMachineKey(name); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "config.json"), b, 0o644); err != nil {
		return err
	}
	fmt.Printf("máquina %s criada (ip %s)\n", name, cfg.IP)
	return nil
}

func machineNames() []string {
	entries, _ := os.ReadDir(machinesDir())
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

func machineList() error {
	names := machineNames()
	if len(names) == 0 {
		fmt.Println("nenhuma máquina")
		return nil
	}
	for _, n := range names {
		cfg, err := loadMachine(n)
		if err != nil {
			continue
		}
		state := "parada"
		if machineRunning(n) {
			state = "rodando"
		}
		fmt.Printf("%-16s %-10s %s\n", n, state, cfg.IP)
	}
	return nil
}

func machineStart(name string) error {
	cfg, err := loadMachine(name)
	if err != nil {
		return err
	}
	if err := ensureMachineKey(name); err != nil {
		return err
	}
	if machineRunning(name) {
		fmt.Printf("%s: já está rodando\n", name)
		return nil
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	_, _, dockerSock := paths()
	dir, _ := os.UserHomeDir()
	_ = dir
	stateDir, _, _ := paths()
	assets := envOr("NANOC_ASSETS", filepath.Join(stateDir, "assets"))

	logf, err := os.OpenFile(machineLog(name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	c := exec.Command(self, "daemon",
		filepath.Join(assets, "Image"), filepath.Join(assets, "initramfs.cpio.gz"),
		machinePath(name), dockerSock, machineCtl(name))
	c.Env = append(os.Environ(),
		"NANOC_GUEST_IP="+cfg.IP,
		"NANOC_NO_DOCKER=1",
		"NANOC_DATA="+machineData(name),
		"NANOC_SHARE_DIR="+stateDir,
		"NANOC_DATA_REL="+filepath.Join("machines", name, "data.img"),
		"NANOC_HOSTNAME="+name,
	)
	c.Stdout, c.Stderr = logf, logf
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := c.Start(); err != nil {
		return err
	}
	for i := 0; i < 90; i++ {
		if machineRunning(name) {
			fmt.Printf("%s: pronta (ip %s)\n", name, cfg.IP)
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("máquina não respondeu; veja %s", machineLog(name))
}

// ensureMachineKey cria o par de chaves e o authorized_keys da máquina.
func ensureMachineKey(name string) error {
	dir := machinePath(name)
	key := filepath.Join(dir, "id_ed25519")
	if _, err := os.Stat(key); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Join(dir, "ssh"), 0o755); err != nil {
		return err
	}
	cmd := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-C", "nanoc-"+name, "-f", key)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ssh-keygen: %v %s", err, out)
	}
	pub, err := os.ReadFile(key + ".pub")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "ssh", "authorized_keys"), pub, 0o600)
}

func machineSSH(name string, extra []string) error {
	cfg, err := loadMachine(name)
	if err != nil {
		return err
	}
	if !machineRunning(name) {
		return fmt.Errorf("máquina %q não está rodando (nanoc machine start %s)", name, name)
	}
	if err := ensureMachineKey(name); err != nil {
		return err
	}
	args := []string{
		"-i", filepath.Join(machinePath(name), "id_ed25519"),
		"-p", "2222",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"root@" + cfg.IP,
	}
	args = append(args, extra...)
	c := exec.Command("ssh", args...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return c.Run()
}

func machineStop(name string) error {
	if _, err := os.Stat(machinePath(name)); err != nil {
		return fmt.Errorf("máquina %q não existe", name)
	}
	if _, err := ctlRequestTo(machineCtl(name), map[string]any{"cmd": "stop"}, 5*time.Second); err != nil {
		fmt.Printf("%s: já está parada\n", name)
		return nil
	}
	for i := 0; i < 100; i++ {
		if !machineRunning(name) {
			fmt.Printf("%s: parada\n", name)
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return nil
}

func machineRemove(name string) error {
	_ = machineStop(name)
	if err := os.RemoveAll(machinePath(name)); err != nil {
		return err
	}
	fmt.Printf("%s: removida\n", name)
	return nil
}

func machineExec(name string, argv []string) error {
	if _, err := loadMachine(name); err != nil {
		return err
	}
	resp, err := ctlRequestTo(machineCtl(name), map[string]any{"cmd": "run", "argv": argv}, 10*time.Minute)
	if err != nil {
		return err
	}
	if out, ok := resp["output"].(string); ok && out != "" {
		fmt.Print(out)
	}
	code := 0
	if c, ok := resp["exit_code"].(float64); ok {
		code = int(c)
	}
	if code != 0 {
		os.Exit(code)
	}
	return nil
}

// ctlRequestTo envia um comando para um socket de controle arbitrário.
func ctlRequestTo(sock string, payload map[string]any, timeout time.Duration) (map[string]any, error) {
	c, err := net.DialTimeout("unix", sock, 5*time.Second)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(timeout))
	if err := json.NewEncoder(c).Encode(payload); err != nil {
		return nil, err
	}
	var resp map[string]any
	if err := json.NewDecoder(c).Decode(&resp); err != nil {
		return nil, err
	}
	return resp, nil
}

var _ = strconv.Itoa
