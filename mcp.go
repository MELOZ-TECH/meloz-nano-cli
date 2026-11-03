package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// runMCP: nanoc mcp. Servidor MCP por stdio (JSON-RPC, uma mensagem por linha)
// para um agente depurar o nanoc com dados em vez de palpite: estado, eventos,
// log do daemon, foto do Mac e comandos no guest.
func runMCP(in io.Reader, out io.Writer) error {
	enc := json.NewEncoder(out)
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(sc.Bytes(), &req) != nil {
			continue
		}
		// notificação (sem id) não tem resposta
		if len(req.ID) == 0 {
			continue
		}
		resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		switch req.Method {
		case "initialize":
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(req.Params, &p)
			if p.ProtocolVersion == "" {
				p.ProtocolVersion = "2025-06-18"
			}
			resp["result"] = map[string]any{
				"protocolVersion": p.ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "nanoc", "version": version},
			}
		case "ping":
			resp["result"] = map[string]any{}
		case "tools/list":
			resp["result"] = map[string]any{"tools": mcpTools}
		case "tools/call":
			var p struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &p)
			text, err := mcpCall(p.Name, p.Arguments)
			if err != nil {
				text = err.Error()
			}
			resp["result"] = map[string]any{
				"content": []map[string]any{{"type": "text", "text": text}},
				"isError": err != nil,
			}
		default:
			resp["error"] = map[string]any{"code": -32601, "message": "método desconhecido: " + req.Method}
		}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return sc.Err()
}

func mcpSchema(props map[string]any) map[string]any {
	return map[string]any{"type": "object", "properties": props}
}

var mcpTools = []map[string]any{
	{"name": "status", "description": "Estado do daemon do nanoc (pid, backend, memória, portas) e se há um daemon vivo sem responder.",
		"inputSchema": mcpSchema(map[string]any{})},
	{"name": "events", "description": "Últimos eventos do daemon em ~/.nanoc/events.log: daemon_start, guest_ready, stop_request (com o processo que pediu), signal, vm_stopped, daemon_exit (com sinal e foto do Mac quando a saída foi anormal).",
		"inputSchema": mcpSchema(map[string]any{
			"limit": map[string]any{"type": "integer", "description": "quantos eventos (padrão 50)"},
			"ev":    map[string]any{"type": "string", "description": "só este tipo de evento"},
		})},
	{"name": "daemon_log", "description": "Fim do ~/.nanoc/daemon.log (console do guest e mensagens do daemon).",
		"inputSchema": mcpSchema(map[string]any{
			"lines": map[string]any{"type": "integer", "description": "quantas linhas (padrão 100)"},
			"grep":  map[string]any{"type": "string", "description": "só linhas com este texto"},
		})},
	{"name": "host_snapshot", "description": "Foto do Mac agora: memória, swap, maiores processos e os que nasceram nos últimos 2 minutos.",
		"inputSchema": mcpSchema(map[string]any{})},
	{"name": "guest_run", "description": "Roda um comando no guest como root, pelo agente (ex.: [\"cat\",\"/proc/meminfo\"]).",
		"inputSchema": map[string]any{"type": "object", "required": []string{"argv"}, "properties": map[string]any{
			"argv": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		}}},
}

func mcpCall(name string, args json.RawMessage) (string, error) {
	var a struct {
		Limit int      `json:"limit"`
		Ev    string   `json:"ev"`
		Lines int      `json:"lines"`
		Grep  string   `json:"grep"`
		Argv  []string `json:"argv"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return "", fmt.Errorf("argumentos inválidos: %v", err)
		}
	}
	stateDir, ctl, _ := paths()
	switch name {
	case "status":
		resp, err := ctlRequest("status", 2*time.Second)
		if err != nil {
			return jsonText(map[string]any{"running": false, "daemon_alive": daemonAlive(ctl),
				"error": err.Error(), "last_events": readEvents(eventsFile(stateDir), 5, "")}), nil
		}
		return jsonText(resp), nil
	case "events":
		if a.Limit <= 0 {
			a.Limit = 50
		}
		return strings.Join(readEvents(eventsFile(stateDir), a.Limit, a.Ev), "\n"), nil
	case "daemon_log":
		if a.Lines <= 0 {
			a.Lines = 100
		}
		return tailFile(filepath.Join(stateDir, "daemon.log"), a.Lines, a.Grep)
	case "host_snapshot":
		return jsonText(hostSnapshot()), nil
	case "guest_run":
		if len(a.Argv) == 0 {
			return "", fmt.Errorf("argv vazio")
		}
		resp, err := ctlRequestRaw(map[string]any{"cmd": "run", "argv": a.Argv}, 60*time.Second)
		if err != nil {
			return "", err
		}
		return jsonText(resp), nil
	}
	return "", fmt.Errorf("ferramenta desconhecida: %s", name)
}

func jsonText(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

// tailFile lê só o fim do arquivo: o daemon.log passa de MBs.
func tailFile(path string, n int, grep string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	const window = 2 << 20
	if st, err := f.Stat(); err == nil && st.Size() > window {
		_, _ = f.Seek(st.Size()-window, io.SeekStart)
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}
	var keep []string
	for _, l := range strings.Split(string(b), "\n") {
		if grep == "" || strings.Contains(l, grep) {
			keep = append(keep, l)
		}
	}
	if len(keep) > n {
		keep = keep[len(keep)-n:]
	}
	return strings.Join(keep, "\n"), nil
}
