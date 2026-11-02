package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/Code-Hex/vz/v3"
	"golang.org/x/sys/unix"
)

// Memória dinâmica. A VM nasce com um teto (NANOC_MEM_MB) e o balão segura o guest
// num alvo menor, que cresce quando ele precisa. O VZ não devolve ao macOS página
// que o guest já usou (o balão da Apple não tem free page reporting), então o custo
// no Mac é o maior alvo já atingido, não o teto. Encolher o alvo ainda ajuda: as
// páginas presas no balão ficam frias e o macOS as comprime ou manda para o swap.

const mib = 1024 * 1024

func envMB(name string, def uint64) uint64 {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil && n >= 256 {
			return n
		}
	}
	return def
}

// memBytes: teto de RAM da VM. NANOC_MEM_MB; padrão: metade da RAM do Mac,
// entre 2 e 8 GB.
func memBytes() uint64 {
	def := uint64(2048)
	if total, err := unix.SysctlUint64("hw.memsize"); err == nil {
		def = min(max(total/mib/2, 2048), 8192)
	}
	return envMB("NANOC_MEM_MB", def) * mib
}

// memFloorMB: alvo inicial e mínimo do balão. NANOC_MEM_MIN_MB (padrão 1024).
func memFloorMB() uint64 {
	return min(envMB("NANOC_MEM_MIN_MB", 1024), memBytes()/mib)
}

// balloon devolve o dispositivo de balão da VM (nil se não houver).
func balloon(vm *vz.VirtualMachine) *vz.VirtioTraditionalMemoryBalloonDevice {
	for _, d := range vm.MemoryBalloonDevices() {
		if b := vz.AsVirtioTraditionalMemoryBalloonDevice(d); b != nil {
			return b
		}
	}
	return nil
}

// guestMem é a amostra que o nanoc-agent devolve no comando "mem".
type guestMem struct {
	TotalKB uint64  `json:"mem_total_kb"`
	AvailKB uint64  `json:"mem_avail_kb"`
	PSI10   float64 `json:"psi_some_avg10"`
}

// nextTarget decide o próximo alvo do balão (MB). idle conta as amostras seguidas
// com folga; quem chama zera o contador quando o alvo muda.
func nextTarget(cur, floor, ceil uint64, g guestMem, hostCritical bool, idle int) uint64 {
	avail := g.AvailKB / 1024
	if avail < max(256, cur/5) || g.PSI10 > 10 {
		step := uint64(512)
		if hostCritical {
			step = 256
		}
		return min(ceil, cur+step)
	}
	if idle >= 30 && avail > cur/2+256 {
		return max(floor, cur-256)
	}
	return cur
}

type memController struct {
	vm          *vz.VirtualMachine
	dev         *vz.VirtioTraditionalMemoryBalloonDevice
	floor, ceil uint64
	target      atomic.Uint64
}

// newMemController põe o balão no alvo mínimo antes do guest crescer.
// Devolve nil se a VM não tiver balão ou o teto já for o mínimo.
func newMemController(vm *vz.VirtualMachine) *memController {
	dev := balloon(vm)
	floor, ceil := memFloorMB(), memBytes()/mib
	if dev == nil || os.Getenv("NANOC_MEM_DYNAMIC") == "0" || floor >= ceil {
		return nil
	}
	m := &memController{vm: vm, dev: dev, floor: floor, ceil: ceil}
	m.set(floor)
	return m
}

func (m *memController) set(mb uint64) {
	m.dev.SetTargetVirtualMachineMemorySize(mb * mib)
	m.target.Store(mb)
}

// run consulta o guest a cada 2 s e ajusta o alvo.
func (m *memController) run() {
	idle := 0
	ready := false
	for range time.Tick(2 * time.Second) {
		g, err := m.sample()
		if err != nil {
			continue
		}
		cur := m.target.Load()
		if !ready {
			// o alvo dado logo após vm.Start() se perde se o driver do balão ainda
			// não subiu; reaplica quando o agente já responde
			ready = true
			m.set(cur)
			fmt.Fprintf(os.Stderr, "%s memória dinâmica ativa: %d MB de até %d MB\n",
				time.Now().Format(time.RFC3339), cur, m.ceil)
			continue
		}
		next := nextTarget(cur, m.floor, m.ceil, g, hostMemCritical(), idle)
		if next == cur {
			idle++
			continue
		}
		idle = 0
		m.set(next)
		fmt.Fprintf(os.Stderr, "%s memória do guest: %d → %d MB (livre %d MB, psi %.1f)\n",
			time.Now().Format(time.RFC3339), cur, next, g.AvailKB/1024, g.PSI10)
	}
}

func (m *memController) sample() (guestMem, error) {
	var g guestMem
	c, err := dialGuest(m.vm, guestControlPort)
	if err != nil {
		return g, err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	if err := json.NewEncoder(c).Encode(map[string]any{"cmd": "mem"}); err != nil {
		return g, err
	}
	if err := json.NewDecoder(c).Decode(&g); err != nil {
		return g, err
	}
	if g.TotalKB == 0 {
		return g, fmt.Errorf("agente sem o comando mem")
	}
	return g, nil
}

func (m *memController) status() map[string]any {
	if m == nil {
		return map[string]any{"mem_max_mb": memBytes() / mib}
	}
	return map[string]any{"mem_target_mb": m.target.Load(), "mem_max_mb": m.ceil}
}

// hostMemCritical: o macOS está em pressão de memória crítica.
func hostMemCritical() bool {
	lvl, err := unix.SysctlUint32("kern.memorystatus_vm_pressure_level")
	return err == nil && lvl >= 4
}

// balloonCmd ajusta (mb > 0) e informa o tamanho alvo da memória do guest.
func balloonCmd(m *memController, mb uint64) map[string]any {
	if m == nil {
		return map[string]any{"ok": false, "message": "memória dinâmica desligada"}
	}
	if mb > 0 {
		m.set(min(max(mb, 256), m.ceil))
	}
	out := m.status()
	out["ok"] = true
	return out
}
