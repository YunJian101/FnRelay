import datetime
import http.server
import json
import os
import shutil
import socket
import ssl
import subprocess
import sys
import threading
import time
import urllib.request

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import make_test_config  # noqa: E402  (每次测试前重置 testdata/config.json)

ROOT = r"C:\Users\SYanB\Documents\deepseek-harness\default-workspace\fnrelay"
DATA = os.path.join(ROOT, "testdata")
APP = os.path.join(ROOT, "build", "fnrelay-test.exe")
CERTDIR = os.path.join(DATA, "certs")
PROXY = 18443
ADMIN = 18899
EXTRA = 18080
UPSTREAM = 18081


class Handler(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        length = int(self.headers.get("Content-Length") or 0)
        received = len(self.rfile.read(length)) if length else 0
        body = json.dumps({"path": self.path, "received": received}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path.startswith("/stream"):
            self.send_response(200)
            self.send_header("Content-Type", "application/octet-stream")
            self.send_header("Content-Length", "10240")
            self.end_headers()
            for _ in range(10):
                self.wfile.write(b"x" * 1024)
                self.wfile.flush()
                time.sleep(0.2)
            return
        if self.path.startswith("/slow"):
            time.sleep(1.5)
        body = json.dumps({
            "host": self.headers.get("Host"),
            "path": self.path,
            "proto": self.headers.get("X-Forwarded-Proto"),
            "xff": self.headers.get("X-Forwarded-For"),
            "real": self.headers.get("X-Real-IP"),
            "port": self.headers.get("X-Forwarded-Port"),
            "xfh": self.headers.get("X-Forwarded-Host"),
        }).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass


def http_get(host, path="/", port=PROXY):
    req = urllib.request.Request("http://127.0.0.1:%d%s" % (port, path), headers={"Host": host})
    opener = urllib.request.build_opener(NoRedirect)
    try:
        r = opener.open(req, timeout=10)
        return r.status, dict(r.headers), r.read().decode()
    except urllib.error.HTTPError as e:
        return e.code, dict(e.headers), e.read().decode()


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def https_get(host, path="/"):
    ctx = ssl.create_default_context()
    ctx.check_hostname = False
    ctx.verify_mode = ssl.CERT_NONE
    conn = socket.create_connection(("127.0.0.1", PROXY), timeout=10)
    ssock = ctx.wrap_socket(conn, server_hostname=host)
    req = "GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n" % (path, host)
    ssock.sendall(req.encode())
    data = b""
    while True:
        chunk = ssock.recv(65536)
        if not chunk:
            break
        data += chunk
    ssock.close()
    head, _, body = data.partition(b"\r\n\r\n")
    lines = head.decode("latin1").split("\r\n")
    status = int(lines[0].split()[1])
    return status, lines, body.decode()


def https_post(host, path, data):
    ctx = ssl.create_default_context()
    ctx.check_hostname = False
    ctx.verify_mode = ssl.CERT_NONE
    conn = socket.create_connection(("127.0.0.1", PROXY), timeout=10)
    ssock = ctx.wrap_socket(conn, server_hostname=host)
    head = "POST %s HTTP/1.1\r\nHost: %s\r\nContent-Length: %d\r\nContent-Type: application/octet-stream\r\nConnection: close\r\n\r\n" % (
        path, host, len(data))
    ssock.sendall(head.encode() + data)
    raw = b""
    while True:
        chunk = ssock.recv(65536)
        if not chunk:
            break
        raw += chunk
    ssock.close()
    header, _, body = raw.partition(b"\r\n\r\n")
    status = int(header.decode("latin1").split("\r\n")[0].split()[1])
    return status, body.decode()


def call(method, path, body=None, cookie=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request("http://127.0.0.1:%d%s" % (ADMIN, path), data=data, method=method)
    req.add_header("X-FnRelay", "1")
    if data:
        req.add_header("Content-Type", "application/json")
    if cookie:
        req.add_header("Cookie", cookie)
    try:
        r = urllib.request.urlopen(req, timeout=10)
        return r.status, dict(r.headers), r.read().decode()
    except urllib.error.HTTPError as e:
        return e.code, dict(e.headers), e.read().decode()


def main():
    shutil.rmtree(os.path.join(DATA, "logs"), ignore_errors=True)
    srv = http.server.ThreadingHTTPServer(("127.0.0.1", UPSTREAM), Handler)
    import threading
    threading.Thread(target=srv.serve_forever, daemon=True).start()

    env = dict(os.environ)
    proc = subprocess.Popen([APP, "--data", DATA, "--admin-port", str(ADMIN)], env=env,
                            stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    time.sleep(1.2)
    if proc.poll() is not None:
        print(proc.stdout.read().decode(errors="replace"))
        return 1

    ok = True

    def check(name, cond, detail=""):
        nonlocal ok
        print(("PASS " if cond else "FAIL ") + name + ((" :: " + str(detail)) if detail else ""))
        if not cond:
            ok = False

    try:
        code, headers, _ = http_get("app.test.local:%d" % PROXY, "/hello?x=1")
        check("HTTP on mixed port redirects 308", code == 308, code)
        check("redirect keeps same port", headers.get("Location") == "https://app.test.local:%d/hello?x=1" % PROXY, headers.get("Location"))

        code, headers, _ = http_get("app.test.local", "/no-port")
        check("Host without port defaults to 443", headers.get("Location") == "https://app.test.local/no-port", headers.get("Location"))

        code, headers, _ = http_get("app.test.local:7777", "/nat")
        check("redirect uses external Host port", headers.get("Location") == "https://app.test.local:7777/nat", headers.get("Location"))

        code, _, body = https_get("app.test.local", "/hello")
        check("HTTPS exact rule proxies", code == 200, code)
        payload = json.loads(body)
        check("upstream sees original Host", payload["host"] == "app.test.local", payload["host"])
        check("X-Forwarded-Proto is https", payload["proto"] == "https", payload["proto"])
        check("X-Forwarded-Port is proxy port", payload["port"] == str(PROXY), payload["port"])

        code, _, body = https_get("x.test.local", "/wild")
        check("HTTPS wildcard rule proxies", code == 200, code)
        payload = json.loads(body)
        check("wildcard upstream host", payload["host"] == "x.test.local", payload["host"])

        code, _, body = https_get("nope.local", "/")
        check("unknown host returns 404 over TLS", code == 404, code)
        leaked = [x for x in ("nope.local", "FnRelay", "飞牛", "反代", "管理后台", "规则") if x in body]
        check("404 page hides host/brand/admin", not leaked, leaked)

        code, headers, _ = http_get("app.test.local", "/", port=EXTRA)
        check("extra HTTP port redirects", code == 308, code)
        check("extra HTTP redirect target", headers.get("Location") == "https://app.test.local:%d/" % PROXY, headers.get("Location"))

        code, _, body = call("GET", "/api/state")
        st = json.loads(body)
        check("state authenticated without password", st.get("authenticated") is True, st)
        check("no setup flow", st.get("needs_setup") is False, st.get("needs_setup"))
        check("three certs loaded", len([c for c in st.get("certs", []) if not c.get("builtin")]) == 3, len(st.get("certs", [])))

        code, _, body = call("POST", "/api/login", {"password": "whatever"})
        check("password login endpoint removed", code in (404, 405), code)

        code, _, body = call("POST", "/api/test", {"host": "app.test.local"})
        tr = json.loads(body)
        check("test matches exact rule", tr.get("rule") == "app", tr)
        check("test selects exact cert", (tr.get("cert") or {}).get("name") == "app.test.local", (tr.get("cert") or {}).get("name"))

        code, _, body = call("POST", "/api/test", {"host": "x.test.local"})
        tr = json.loads(body)
        check("test matches wildcard rule", tr.get("rule") == "wild", tr)
        check("test selects wildcard cert", "*.test.local" in ((tr.get("cert") or {}).get("domains") or []), (tr.get("cert") or {}).get("domains"))

        code, _, body = call("POST", "/api/rules", {
            "name": "other", "enabled": True, "domains": ["other.local"],
            "upstream": "http://127.0.0.1:%d" % UPSTREAM,
        })
        check("create rule", code == 200, body)

        code, _, body = call("GET", "/api/state")
        st = json.loads(body)
        check("rule persisted", any(r["name"] == "other" for r in st["config"]["rules"]))

        code, _, body = https_get("other.local", "/")
        check("new rule works without restart", code == 200, code)

        code, _, _ = https_get("deep.sub.test.local", "/wild")
        check("multi-level wildcard rule matches", code == 200, code)

        code, headers, body = call("GET", "/")
        check("admin UI index served", code == 200 and "FnRelay" in body, code)
        code, headers, body = call("GET", "/assets/app.js")
        check("admin UI assets served", code == 200 and "FnRelay" in body, code)

        state = json.loads(call("GET", "/api/state")[2])
        cfg = state["config"]
        code, _, body = call("POST", "/api/settings", {
            "proxy": dict(cfg["proxy"], redirect_mode="custom_port", redirect_port=9443),
            "certs": cfg["certs"],
        })
        check("save custom redirect settings", code == 200, body)
        code, headers, _ = http_get("app.test.local", "/x")
        check("custom port redirect target", headers.get("Location") == "https://app.test.local:9443/x", headers.get("Location"))

        state = json.loads(call("GET", "/api/state")[2])
        cfg = state["config"]
        code, _, body = call("POST", "/api/settings", {
            "proxy": dict(cfg["proxy"], http_redirect=False),
            "certs": cfg["certs"],
        })
        check("disable redirect", code == 200, body)
        code, headers, body = http_get("app.test.local", "/plain")
        check("plain HTTP proxies when redirect off", code == 200, code)
        if code == 200:
            check("plain HTTP forwarded as http", json.loads(body)["proto"] == "http", json.loads(body)["proto"])

        # 本地日志：开启落盘、访问 IP/来源 IP、审核事件、查询参数打码、按天文件
        state = json.loads(call("GET", "/api/state")[2])
        cfg = state["config"]
        code, _, body = call("POST", "/api/settings", {
            "proxy": dict(cfg["proxy"]),
            "certs": cfg["certs"],
            "logs": {
                "file_enabled": True,
                "access": True,
                "audit": True,
                "retention_days": 7,
                "max_file_mb": 100,
            },
        })
        check("enable local logs", code == 200, body)
        state = json.loads(call("GET", "/api/state")[2])
        check("log stats enabled", state.get("log_stats", {}).get("enabled") is True, state.get("log_stats"))

        code, _, body = call("POST", "/api/rules", {
            "name": "logged", "enabled": True, "domains": ["logged.local"],
            "upstream": "http://127.0.0.1:%d" % UPSTREAM,
        })
        check("create audited rule", code == 200, body)
        rule_id = json.loads(body).get("id") if code == 200 else ""

        https_get("app.test.local", "/secret?token=abc123&foo=1")
        time.sleep(0.5)
        today = datetime.date.today().isoformat()

        code, _, body = call("GET", "/api/logs?date=%s&kind=access" % today)
        logs = json.loads(body).get("logs", []) if code == 200 else []
        secret = next((x for x in logs if x.get("path") == "/secret"), None)
        check("access log has client IP", bool(secret and secret.get("client_ip")), secret)
        check("access log has IP source", bool(secret and secret.get("ip_source")), secret)
        check("access query token redacted", bool(secret and "REDACTED" in (secret.get("query") or "") and "abc123" not in (secret.get("query") or "")), secret.get("query") if secret else None)

        code, _, body = call("GET", "/api/logs?date=%s&kind=audit" % today)
        audits = json.loads(body).get("logs", []) if code == 200 else []
        check("audit log has settings.save", any(x.get("action") == "settings.save" for x in audits), [x.get("action") for x in audits])
        check("audit log has rule.create", any(x.get("action") == "rule.create" and x.get("target") == rule_id for x in audits), rule_id)
        check("audit log has actor", any((x.get("actor") or {}).get("uid") == "dev" for x in audits), audits[:1])

        code, _, body = call("GET", "/api/logs/dates")
        dates = json.loads(body).get("dates", []) if code == 200 else []
        check("log dates include today", any(x.get("date") == today for x in dates), dates)

        req = urllib.request.Request("http://127.0.0.1:%d/api/logs/download?date=%s&kind=access" % (ADMIN, today))
        with urllib.request.urlopen(req, timeout=8) as r:
            download = r.read().decode("utf-8", "replace")
            disp = r.headers.get("Content-Disposition", "")
        check("download access log", "REDACTED" in download and "attachment" in disp, disp)

        slow_result = []
        slow_thread = threading.Thread(target=lambda: slow_result.append(https_get("app.test.local", "/slow")))
        slow_thread.start()
        time.sleep(0.3)
        live = json.loads(call("GET", "/api/live")[2])
        active = next((x for x in live.get("requests", []) if x.get("path") == "/slow"), None)
        check("live request appears", bool(active), live)
        check("live request has ip", bool(active and active.get("client_ip")), active)
        check("live request has upstream", bool(active and active.get("upstream_host") == "127.0.0.1:18081"), active)
        stats_active = json.loads(call("GET", "/api/stats")[2])
        check("stats active requests while streaming", stats_active.get("active_requests", 0) >= 1, stats_active)
        slow_thread.join(timeout=5)
        time.sleep(0.3)
        live2 = json.loads(call("GET", "/api/live")[2])
        check("live request removed after finish", not any(x.get("path") == "/slow" for x in live2.get("requests", [])), live2)

        before_stream = json.loads(call("GET", "/api/state")[2])["status"]
        stream_result = []
        stream_thread = threading.Thread(target=lambda: stream_result.append(https_get("app.test.local", "/stream")))
        stream_thread.start()
        time.sleep(0.5)
        mid_stream = json.loads(call("GET", "/api/state")[2])["status"]
        check("stats requests grows during stream", mid_stream.get("requests", 0) > before_stream.get("requests", 0), (before_stream, mid_stream))
        check("stats bytes_out grows during stream", mid_stream.get("bytes_out", 0) > before_stream.get("bytes_out", 0), (before_stream, mid_stream))
        call("GET", "/api/live")
        time.sleep(0.6)
        live3 = json.loads(call("GET", "/api/live")[2])
        stream_entry = next((x for x in live3.get("requests", []) if x.get("path") == "/stream"), None)
        check("live stream has bytes", bool(stream_entry and stream_entry.get("down_bytes", 0) > 0), stream_entry)
        check("live stream has speed", bool(stream_entry and stream_entry.get("down_bps", 0) > 0), stream_entry)
        stream_thread.join(timeout=5)

        code, _, body = call("GET", "/api/stats")
        stats_api = json.loads(body) if code == 200 else {}
        check("stats API returns counters", code == 200 and "requests" in stats_api and "bytes_out" in stats_api, stats_api)

        before_in = json.loads(call("GET", "/api/state")[2])["status"]["bytes_in"]
        payload = b"z" * 4096
        code, body = https_post("app.test.local", "/upload", payload)
        upload = json.loads(body) if code == 200 else {}
        check("upload proxies", code == 200 and upload.get("received") == len(payload), upload)
        after_in = json.loads(call("GET", "/api/state")[2])["status"]["bytes_in"]
        check("bytes_in counts request body", after_in - before_in >= len(payload), (before_in, after_in))

        current_version = json.loads(call("GET", "/api/state")[2]).get("version")
        code, headers, body = call("GET", "/api/backup")
        backup = json.loads(body) if code == 200 else {}
        check("backup has version", backup.get("version") == current_version, backup.get("version"))
        check("backup has rules", len(backup.get("config", {}).get("rules", [])) > 0, backup.get("config", {}).get("rules"))
        check("backup is attachment", "attachment" in headers.get("Content-Disposition", ""), headers.get("Content-Disposition"))
        bad = dict(backup)
        bad["version"] = "0.0.0"
        code, _, body = call("POST", "/api/restore", bad)
        check("restore rejects version mismatch", code == 400 and "版本不一致" in body, body)
        code, _, body = call("POST", "/api/restore", backup)
        check("restore accepts matching backup", code == 200, body)

        for name, domain in (("batch1", "1.s.com"), ("batch2", "2.s.com")):
            code, _, body = call("POST", "/api/rules", {
                "name": name, "enabled": True, "domains": [domain],
                "upstream": "http://127.0.0.1:%d" % UPSTREAM,
            })
            check("create " + name, code == 200, body)
        code, _, body = call("POST", "/api/domains/replace", {"old_suffix": "s.com", "new_suffix": "c.com"})
        result = json.loads(body) if code == 200 else {}
        check("batch replace domains", code == 200 and result.get("changed_domains") == 2, body)
        state = json.loads(call("GET", "/api/state")[2])
        domains = [d for r in state["config"]["rules"] for d in r["domains"]]
        check("batch replace result domains", "1.c.com" in domains and "2.c.com" in domains and "1.s.com" not in domains, domains)
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=5)
        except Exception:
            proc.kill()
        srv.shutdown()
    return 0 if ok else 2


if __name__ == "__main__":
    sys.exit(main())
