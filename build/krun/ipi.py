import os, time
def pingpong(a, b, n=3000):
    r1, w1 = os.pipe(); r2, w2 = os.pipe()
    pid = os.fork()
    if pid == 0:
        os.sched_setaffinity(0, {b})
        for _ in range(n):
            os.read(r1, 1); os.write(w2, b"x")
        os._exit(0)
    os.sched_setaffinity(0, {a})
    time.sleep(0.2)
    t = time.perf_counter()
    for _ in range(n):
        os.write(w1, b"x"); os.read(r2, 1)
    dt = (time.perf_counter() - t) / n * 1e6
    os.waitpid(pid, 0)
    return dt
print("ida e volta na mesma CPU:   %6.1f µs" % pingpong(0, 0))
print("ida e volta entre 2 CPUs:   %6.1f µs" % pingpong(0, 1))
