import json
import os
import subprocess
import time

ROOT = r"C:\Users\SYanB\Documents\deepseek-harness\default-workspace\fnrelay"
DONE = os.path.join(ROOT, "build", "oauth194.done")
CLI = r"C:\Users\SYanB\.dsh\skills\fnos\bin\trim-cli-windows-x64.exe"
HOST = "192.168.11.194"

deadline = time.time() + 600
while time.time() < deadline:
    if os.path.exists(DONE):
        break
    time.sleep(1)
else:
    print("TIMEOUT waiting for oauth194.done")
    raise SystemExit(1)

status = open(DONE, encoding="utf-8").read().strip()
print("login status:", status)
if not (status.lower() == "ok" or status.lower() == "exit=0"):
    raise SystemExit(2)

base = [CLI, "--profile", "fn194", "--host", HOST, "--scheme", "https", "--tls-insecure"]
for args, name in [
    (["system", "info"], "system194.json"),
    (["app", "list"], "applist194.json"),
    (["file", "ls", "/usr/trim/etc"], "trim_etc194.json"),
]:
    proc = subprocess.run(base + args, capture_output=True, text=True, encoding="utf-8", errors="replace")
    print("==", name, "exit", proc.returncode)
    out = proc.stdout.strip()
    print(out[:6000])
    if proc.stderr.strip():
        print("stderr:", proc.stderr.strip()[:2000])
    with open(os.path.join(ROOT, "build", name), "w", encoding="utf-8") as f:
        f.write(out)
