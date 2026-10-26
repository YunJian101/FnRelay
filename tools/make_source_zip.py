import os
import zipfile

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
OUT = os.path.join(ROOT, "dist", "FnRelay-1.0.24-source.zip")

EXCLUDE_DIRS = {".git", "__pycache__", "build", "dist", "testdata", "tools/go", "tools/gocache"}
EXCLUDE_FILES = {"tools/fnpack.exe", "tools/gettools.py"}


def add_dir(zf, rel):
    abs_dir = os.path.join(ROOT, rel)
    for base, dirs, files in os.walk(abs_dir):
        keep = []
        for d in dirs:
            relpath = os.path.relpath(os.path.join(base, d), ROOT).replace("\\", "/")
            if relpath in EXCLUDE_DIRS or d in (".git", "__pycache__"):
                continue
            keep.append(d)
        dirs[:] = keep
        for name in files:
            full = os.path.join(base, name)
            relpath = os.path.relpath(full, ROOT).replace("\\", "/")
            if relpath in EXCLUDE_FILES:
                continue
            zf.write(full, relpath)


with zipfile.ZipFile(OUT, "w", zipfile.ZIP_DEFLATED, compresslevel=9) as zf:
    for name in ("README.md", "LICENSE", "build.py"):
        zf.write(os.path.join(ROOT, name), name)
    for d in ("src", "packaging", "docs", "tools"):
        add_dir(zf, d)

print(OUT, os.path.getsize(OUT))
