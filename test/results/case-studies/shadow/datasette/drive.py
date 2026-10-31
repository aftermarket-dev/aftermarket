# Sends every path in paths.txt to BASE_URL, ROUNDS times, one after another,
# and prints the statuses the client got. Usage: drive.py BASE_URL ROUNDS PATHS_FILE
import sys, urllib.request, urllib.error
base, rounds = sys.argv[1], int(sys.argv[2])
paths = [l.strip() for l in open(sys.argv[3]) if l.strip()]
codes = {}
for _ in range(rounds):
    for p in paths:
        try:
            with urllib.request.urlopen(base + p, timeout=10) as r:
                r.read(); c = r.status
        except urllib.error.HTTPError as e:
            e.read(); c = e.code
        codes[c] = codes.get(c, 0) + 1
print("requests:", sum(codes.values()), "statuses:", dict(sorted(codes.items())))
