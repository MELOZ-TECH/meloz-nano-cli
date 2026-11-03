# mede quanta RAM o processo da VM ocupa no Mac antes, durante e depois de o guest
# usar e liberar 1,2 GB. uso: python3 -I measure.py <nanoc-krun> <estado> <fpr> <saida.json>
import json, os, re, signal, socket, subprocess, sys, time

nanoc, state, fpr, out = sys.argv[1:5]
K = os.path.dirname(os.path.abspath(__file__))
env = dict(os.environ, NANOC_KRUN_INITRD=os.path.join(K, "initramfs-krun.cpio.gz"))
log = open(os.path.join(K, f"boot-{fpr}.log"), "w")
p = subprocess.Popen(["taskpolicy", "-b", nanoc, "krun-boot", "-dir", state, "-ram", "2048", "-fpr", fpr],
                     stdout=log, stderr=subprocess.STDOUT, env=env)
ctl = os.path.expanduser("~/.nanoc-krun/vsock-2376.sock")


def guest(argv, timeout=120):
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
    # o libkrun aceita no socket do host mesmo sem o agente escutando e fecha vazio
    return json.loads(d) if d.strip() else {"ok": False}


def host_mem():
    rss = int(subprocess.run(["ps", "-o", "rss=", "-p", str(p.pid)], capture_output=True, text=True).stdout.strip() or 0)
    fp = subprocess.run(["footprint", "-p", str(p.pid)], capture_output=True, text=True).stdout
    m = re.search(r"Footprint:\s+([\d.]+)\s+([KMG]B)", fp)
    phys = None
    if m:
        phys = float(m.group(1)) * {"KB": 1 / 1024, "MB": 1, "GB": 1024}[m.group(2)]
    top = subprocess.run(["top", "-l", "1", "-pid", str(p.pid), "-stats", "mem"], capture_output=True, text=True).stdout.split()
    tm = top[-1] if top else ""
    mm = re.match(r"([\d.]+)([KMG])", tm)
    topmb = round(float(mm.group(1)) * {"K": 1 / 1024, "M": 1, "G": 1024}[mm.group(2)]) if mm else None
    return {"rss_mb": round(rss / 1024), "top_mem_mb": topmb, "footprint_mb": round(phys) if phys else None}


res = {"fpr": fpr, "steps": []}
try:
    t0 = time.time()
    while time.time() - t0 < 120:
        if p.poll() is not None:
            raise SystemExit(f"VM saiu com {p.returncode}; ver boot-{fpr}.log")
        try:
            r = guest(["true"], timeout=5)
            if r.get("ok"):
                break
        except OSError:
            pass
        time.sleep(1)
    else:
        raise SystemExit("guest não respondeu em 120 s")
    res["boot_s"] = round(time.time() - t0, 1)
    time.sleep(10)

    def step(name):
        g = guest(["sh", "-c", "grep -E 'MemFree|MemAvailable' /proc/meminfo | tr -s ' ' | tr '\\n' ' '"])
        res["steps"].append({"step": name, "t": round(time.time() - t0), **host_mem(), "guest": g.get("output", "").strip()})
        print(res["steps"][-1], flush=True)

    step("ocioso")
    guest(["sh", "-c", "dd if=/dev/urandom of=/tmp/big bs=1M count=1200 2>&1 | tail -1"], timeout=300)
    step("1,2 GB aleatórios em tmpfs")
    guest(["sh", "-c", "rm -f /tmp/big; sync; echo 3 > /proc/sys/vm/drop_caches"])
    for s in (5, 15, 30):
        time.sleep(s - (0 if s == 5 else {15: 5, 30: 15}[s]))
        step(f"{s} s depois de apagar")
finally:
    p.send_signal(signal.SIGTERM)
    try:
        p.wait(20)
    except subprocess.TimeoutExpired:
        p.kill()
    json.dump(res, open(out, "w"), indent=1, ensure_ascii=False)
