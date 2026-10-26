import argparse
import os
import shutil
import subprocess
import sys
import time

try:
    sys.stdout.reconfigure(encoding="utf-8")
    sys.stderr.reconfigure(encoding="utf-8")
except Exception:
    pass

ROOT = os.path.dirname(os.path.abspath(__file__))
GO = os.path.join(ROOT, "tools", "go", "bin", "go.exe")
FNPACK = os.path.join(ROOT, "tools", "fnpack.exe")
SRC = os.path.join(ROOT, "src")
PKG = os.path.join(ROOT, "packaging", "fnrelay")
BUILD = os.path.join(ROOT, "build")
DIST = os.path.join(ROOT, "dist")
VERSION = "1.0.24"

ARCHES = {
    "x86": ("amd64", "x86"),
    "arm": ("arm64", "arm"),
}


def run(cmd, cwd=None, env=None):
    print(">", " ".join(cmd))
    proc = subprocess.run(cmd, cwd=cwd, env=env, text=True, capture_output=True)
    if proc.stdout.strip():
        print(proc.stdout.strip())
    if proc.returncode != 0:
        print(proc.stderr.strip(), file=sys.stderr)
        raise SystemExit("命令失败: %s" % " ".join(cmd))
    return proc.stdout


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--arch", default="all", choices=["all", "x86", "arm"])
    parser.add_argument("--version", default=VERSION)
    args = parser.parse_args()

    os.makedirs(BUILD, exist_ok=True)
    os.makedirs(DIST, exist_ok=True)
    subprocess.run([sys.executable, os.path.join(ROOT, "tools", "make_icons.py")], check=True)

    env = dict(os.environ)
    env.update({
        "GOOS": "linux",
        "CGO_ENABLED": "0",
        "GOCACHE": os.path.join(BUILD, "gocache"),
        "GOPATH": os.path.join(BUILD, "gopath"),
        "GOTMPDIR": os.path.join(BUILD, "tmp"),
    })
    for d in ("gocache", "gopath", "tmp"):
        os.makedirs(os.path.join(BUILD, d), exist_ok=True)

    arches = ["x86", "arm"] if args.arch == "all" else [args.arch]
    stamp = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())

    for arch in arches:
        goarch, platform = ARCHES[arch]
        stage = os.path.join(BUILD, "stage-" + arch)
        if os.path.exists(stage):
            shutil.rmtree(stage)
        shutil.copytree(PKG, stage)
        os.makedirs(os.path.join(stage, "app", "server"), exist_ok=True)
        os.makedirs(os.path.join(stage, "wizard"), exist_ok=True)

        target = os.path.join(stage, "app", "server", "fnrelay")
        e = dict(env)
        e["GOARCH"] = goarch
        run([GO, "build", "-trimpath", "-ldflags",
             "-s -w -X main.BuildTime=%s -X main.Version=%s" % (stamp, args.version),
             "-o", target, "."], cwd=SRC, env=e)
        size = os.path.getsize(target)
        print("  binary: %.1f MB" % (size / 1048576.0))

        manifest = os.path.join(stage, "manifest")
        with open(manifest, "r", encoding="utf-8") as f:
            text = f.read()
        text = text.replace("version               = " + VERSION, "version               = " + args.version)
        text = text.replace("platform              = x86", "platform              = " + platform)
        with open(manifest, "w", encoding="utf-8", newline="\n") as f:
            f.write(text)

        run([FNPACK, "build", "--directory", stage], cwd=stage)
        produced = os.path.join(stage, "fnrelay.fpk")
        if not os.path.exists(produced):
            raise SystemExit("fnpack 未生成 fpk: " + produced)
        out = os.path.join(DIST, "FnRelay-%s-%s.fpk" % (args.version, arch))
        shutil.copy2(produced, out)
        print("  package: %s (%.1f MB)" % (out, os.path.getsize(out) / 1048576.0))

    print("\n完成，输出目录: " + DIST)


if __name__ == "__main__":
    main()
