import os
import subprocess
import sys
import threading
import time

ROOT = r"C:\Users\SYanB\Documents\deepseek-harness\default-workspace\fnrelay"
CLI = r"C:\Users\SYanB\.dsh\skills\fnos\bin\trim-cli-windows-x64.exe"
LOG = os.path.join(ROOT, "build", "oauth194.log")
CODE = os.path.join(ROOT, "build", "oauth194.code")

for path in (LOG, CODE):
    try:
        os.remove(path)
    except FileNotFoundError:
        pass
open(LOG, "w", encoding="utf-8").close()

cmd = [
    CLI,
    "--profile", "fn194",
    "--host", "192.168.11.194",
    "--scheme", "https",
    "--tls-insecure",
    "login",
    "--no-open",
]
proc = subprocess.Popen(
    cmd,
    stdin=subprocess.PIPE,
    stdout=subprocess.PIPE,
    stderr=subprocess.STDOUT,
    text=True,
    encoding="utf-8",
    errors="replace",
    bufsize=1,
)


def reader():
    with open(LOG, "a", encoding="utf-8") as f:
        while True:
            ch = proc.stdout.read(1)
            if not ch:
                break
            f.write(ch)
            f.flush()


threading.Thread(target=reader, daemon=True).start()

fed = False
while proc.poll() is None:
    if not fed and os.path.exists(CODE):
        code = open(CODE, encoding="utf-8").read().strip()
        if code:
            try:
                proc.stdin.write(code + "\n")
                proc.stdin.flush()
                fed = True
                with open(LOG, "a", encoding="utf-8") as f:
                    f.write("\n[helper] authorization code submitted\n")
            except Exception as exc:  # noqa: BLE001
                with open(LOG, "a", encoding="utf-8") as f:
                    f.write("\n[helper] submit failed: %r\n" % (exc,))
    time.sleep(0.3)

with open(LOG, "a", encoding="utf-8") as f:
    f.write("\n[helper] exit code %s\n" % proc.returncode)
