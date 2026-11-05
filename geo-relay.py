#!/usr/bin/env python3
"""geo-relay — give Chromium an authenticated upstream proxy.

Chrome ignores user:pass inside --proxy-server, so a credentialed upstream can't
be used directly. This listens on 127.0.0.1:PORT and forwards every CONNECT /
plain request to the upstream with a Proxy-Authorization header added.

    UP_PROXY='http://user-country-US-session-id-lifetime-60:pass@proxy.geonode.io:10000' \
      ./geo-relay.py &
    chrome --proxy-server=127.0.0.1:9999 ...
    playwright: proxy={"server": "http://127.0.0.1:9999"}   # no username/password needed

Playwright takes proxy credentials natively, so the relay is only for clients
that cannot (Chrome CLI, some HTTP stacks).
"""
import base64
import os
import socket
import sys
import threading
import urllib.parse

PORT = int(os.environ.get("PORT", "9999"))
UP_URL = os.environ.get(
    "UP_PROXY",
    "http://geonode_gMweMNW9Hr-country-US-session-jcrelay01-lifetime-60:"
    "3f2b2585-d325-4ae6-b1ac-41f6d7330453@proxy.geonode.io:10000",
)

u = urllib.parse.urlsplit(UP_URL)
if not u.hostname or not u.port:
    sys.exit("UP_PROXY needs host:port")
UP = (u.hostname, u.port)
AUTH = b"Basic " + base64.b64encode(
    f"{urllib.parse.unquote(u.username or '')}:{urllib.parse.unquote(u.password or '')}".encode()
)


def relay(a, b):
    def cp(x, y):
        try:
            while True:
                d = x.recv(65536)
                if not d:
                    break
                y.sendall(d)
        except OSError:
            pass
        finally:
            try:
                y.shutdown(socket.SHUT_WR)
            except OSError:
                pass

    t = threading.Thread(target=cp, args=(b, a), daemon=True)
    t.start()
    cp(a, b)
    t.join(5)


def handle(c):
    try:
        data = b""
        while b"\r\n\r\n" not in data:
            d = c.recv(65536)
            if not d:
                return
            data += d
        head, rest = data.split(b"\r\n\r\n", 1)
        lines = [l for l in head.split(b"\r\n") if not l.lower().startswith(b"proxy-authorization:")]
        up = socket.create_connection(UP, timeout=20)
        up.sendall(b"\r\n".join(lines) + b"\r\nProxy-Authorization: " + AUTH + b"\r\n\r\n")
        if lines[0].split()[0] == b"CONNECT":
            resp = b""
            while b"\r\n\r\n" not in resp:
                d = up.recv(65536)
                if not d:
                    break
                resp += d
            c.sendall(resp)
            if b" 200" not in resp.split(b"\r\n")[0]:
                return
        else:
            up.sendall(rest)
        relay(c, up)
    except Exception as e:  # one bad client must not take the listener down
        print("relay:", e, file=sys.stderr)
    finally:
        try:
            c.close()
        except OSError:
            pass


s = socket.socket()
s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(("127.0.0.1", PORT))
s.listen(64)
print(f"geo-relay on 127.0.0.1:{PORT} -> {UP[0]}:{UP[1]}", flush=True)
while True:
    c, _ = s.accept()
    threading.Thread(target=handle, args=(c,), daemon=True).start()
