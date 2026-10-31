# An old client written against json-server 0.17: reads, then writes, then
# reads back what it created using the id the server gave it.
# Usage: client.py BASE_URL
import gzip, json, sys, urllib.request, urllib.error
base = sys.argv[1]

def call(method, path, body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(base + path, data=data, method=method)
    req.add_header("Accept-Encoding", "gzip")
    if data is not None:
        req.add_header("Content-Type", "application/json")
    try:
        r = urllib.request.urlopen(req, timeout=10)
    except urllib.error.HTTPError as e:
        r = e
    raw = r.read()
    if r.headers.get("Content-Encoding") == "gzip":
        raw = gzip.decompress(raw)
    print(f"{r.status} {method} {path}")
    try:
        return json.loads(raw) if raw else None
    except ValueError:
        return None

call("GET", "/posts")
call("GET", "/posts/1")
call("GET", "/posts?authorId=1")
call("GET", "/posts?_sort=views&_order=desc")
call("GET", "/posts?_page=1&_limit=2")
call("GET", "/posts?title_like=report")
call("GET", "/posts/1/comments")
call("GET", "/posts?_embed=comments")
call("GET", "/posts/99")
call("GET", "/profile")
created = call("POST", "/posts", {"title": "Release notes for 2.0", "views": 0, "authorId": 2})
call("PATCH", "/posts/2", {"views": 86})
call("PUT", "/profile", {"name": "aftermarket"})
call("DELETE", "/comments/3")
call("GET", "/comments")
new_id = created["id"]
call("GET", f"/posts/{new_id}")
call("PATCH", f"/posts/{new_id}", {"views": 1})
call("DELETE", f"/posts/{new_id}")
call("GET", "/posts")
