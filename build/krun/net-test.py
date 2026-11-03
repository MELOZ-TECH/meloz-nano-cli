# sobe o krun-boot com rede e Docker e testa DNS, HTTP, Mac pelo .254, docker pull/run e port forward
# uso: python3 -I net-test.py <nanoc-krun> <estado> <initramfs>
import json, os, signal, socket, subprocess, sys, time, urllib.request

nanoc, state, initrd = sys.argv[1:4]
K = os.path.dirname(os.path.abspath(__file__))
env = dict(os.environ, NANOC_KRUN_INITRD=initrd)
log = open(os.path.join(K, "boot-net.log"), "w")
p = subprocess.Popen(["taskpolicy", "-b", nanoc, "krun-boot", "-dir", state, "-ram", "2048", "-nodocker=false"],
                     stdout=log, stderr=subprocess.STDOUT, env=env)
sd = os.path.expanduser("~/.nanoc-krun")
ctl, dock = sd + "/vsock-2376.sock", sd + "/vsock-2375.sock"


def guest(argv, timeout=180):
    s = socket.socket(socket.AF_UNIX)
    s.settimeout(timeout)
    s.connect(ctl)
    s.sendall((json.dumps({"cmd": "run", "argv": argv}) + "\n").encode())
    d = b""
    while True:
        c = s.recv(65536)
        if not c:
            break
        d += c
    return json.loads(d) if d.strip() else {"ok": False}


def sh(name, cmd, timeout=180):
    t = time.time()
    r = guest(["sh", "-c", cmd + " 2>&1"], timeout)
    print(f"[{name}] ok={r.get('ok')} {time.time()-t:.1f}s :: {r.get('output', r).strip()[-400:] if isinstance(r.get('output'), str) else r}", flush=True)
    return r


# servidor no Mac para o guest alcançar pelo .254
srv = subprocess.Popen([sys.executable, "-I", "-m", "http.server", "8765", "--bind", "127.0.0.1", "--directory", K],
                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
try:
    t0 = time.time()
    while time.time() - t0 < 120:
        if p.poll() is not None:
            raise SystemExit(f"VM saiu com {p.returncode}; ver boot-net.log")
        try:
            if guest(["true"], 5).get("ok"):
                break
        except OSError:
            pass
        time.sleep(1)
    else:
        raise SystemExit("guest não respondeu em 120 s")
    print(f"boot {time.time()-t0:.1f}s", flush=True)
    sh("ip", "ip -4 addr show eth0 | grep inet; ip route")
    sh("ping gw", "ping -c 2 -W 2 192.168.64.1")
    sh("dns", "nslookup example.com 1.1.1.1 | tail -3")
    sh("http", "wget -qO- -T 15 http://example.com | head -c 120")
    sh("https", "wget -qO- -T 15 https://www.google.com | head -c 60")
    sh("mac .254", "wget -qO- -T 10 http://192.168.64.254:8765/net-test.py | head -1")
    sh("download 50MB", "wget -qO /dev/null -T 60 https://speed.cloudflare.com/__down?bytes=50000000 && echo ok", 240)
    # Docker
    t = time.time()
    while time.time() - t < 120:
        r = guest(["sh", "-c", "docker info --format '{{.ServerVersion}}' 2>&1"], 10)
        if r.get("ok") and r.get("output", "").strip()[:1].isdigit():
            break
        time.sleep(2)
    print(f"[dockerd] {r.get('output','').strip()} em {time.time()-t:.1f}s", flush=True)
    sh("docker pull", "docker pull -q nginx:alpine", 300)
    sh("docker run", "docker run -d --name web -p 8080:80 nginx:alpine")
    time.sleep(6)  # o port forward sincroniza a cada 2 s
    try:
        body = urllib.request.urlopen("http://127.0.0.1:8080/", timeout=10).read()
        print("[port forward] Mac -> 127.0.0.1:8080:", b"nginx" in body, flush=True)
    except Exception as e:
        print("[port forward] falhou:", e, flush=True)
    sh("container sai p/ internet", "docker run --rm alpine:latest wget -qO- -T 15 http://example.com | head -c 60", 300)
finally:
    srv.terminate()
    p.send_signal(signal.SIGTERM)
    try:
        p.wait(20)
    except subprocess.TimeoutExpired:
        p.kill()
