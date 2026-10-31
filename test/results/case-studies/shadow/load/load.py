# Keep-alive load: THREADS clients send TOTAL requests between them, cycling
# through paths.txt. Prints statuses, throughput and latency percentiles.
# Usage: load.py HOST PORT THREADS TOTAL PATHS_FILE
import http.client, sys, threading, time
host, port, threads, total = sys.argv[1], int(sys.argv[2]), int(sys.argv[3]), int(sys.argv[4])
paths = [l.strip() for l in open(sys.argv[5]) if l.strip()]
lock = threading.Lock(); lat = []; codes = {}; n = [0]
def worker():
    c = http.client.HTTPConnection(host, port, timeout=30)
    while True:
        with lock:
            if n[0] >= total: return
            i = n[0]; n[0] += 1
        t = time.perf_counter()
        c.request("GET", paths[i % len(paths)])
        r = c.getresponse(); r.read()
        d = time.perf_counter() - t
        with lock:
            lat.append(d); codes[r.status] = codes.get(r.status, 0) + 1
start = time.perf_counter()
ts = [threading.Thread(target=worker) for _ in range(threads)]
[t.start() for t in ts]; [t.join() for t in ts]
el = time.perf_counter() - start
lat.sort()
p = lambda q: lat[min(len(lat) - 1, int(q * len(lat)))] * 1000
print(f"requests: {len(lat)} statuses: {dict(sorted(codes.items()))} threads: {threads}")
print(f"elapsed: {el:.1f}s throughput: {len(lat)/el:.0f} req/s")
print(f"latency ms: p50 {p(0.5):.1f} p90 {p(0.9):.1f} p99 {p(0.99):.1f} max {lat[-1]*1000:.1f}")
