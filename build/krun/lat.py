import os, sys, time
d = sys.argv[1]
def per(n, f):
    t = time.perf_counter()
    for i in range(n): f(i)
    return (time.perf_counter() - t) / n * 1e6
tag = str(time.time_ns())
def neg(i):
    try: os.stat(f"{d}/nao-{tag}-{i}")
    except FileNotFoundError: pass
def mk(i):
    fd = os.open(f"{d}/f-{tag}-{i}", os.O_CREAT | os.O_WRONLY, 0o644); os.close(fd)
def rm(i): os.unlink(f"{d}/f-{tag}-{i}")
def getpid(i): os.getpid()
print("syscall vazio:         %6.1f µs" % per(20000, getpid))
print("lookup negativo:       %6.1f µs" % per(int(os.environ.get("N","3000")), neg))
print("criar arquivo:         %6.1f µs" % per(1000, mk))
print("apagar arquivo:        %6.1f µs" % per(1000, rm))
