import hashlib
import io
import os
import sys
import tarfile

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DIST = os.path.join(ROOT, "dist")


def check(arch, machine):
    path = os.path.join(DIST, "FnRelay-1.0.24-%s.fpk" % arch)
    with tarfile.open(path, "r:gz") as tf:
        names = tf.getnames()
        manifest = tf.extractfile("manifest").read()
        app_tgz = tf.extractfile("app.tgz").read()
        main = tf.extractfile("cmd/main").read()
        priv = tf.extractfile("config/privilege").read()
    text = manifest.decode("utf-8")
    print("==", arch, os.path.getsize(path), "bytes")
    for line in text.splitlines():
        if line.startswith(("appname", "version", "display_name", "platform", "service_port", "checkport", "checksum")):
            print("  " + line)
    md5 = hashlib.md5(app_tgz).hexdigest()
    checksum = [l.split("=", 1)[1].strip() for l in text.splitlines() if l.startswith("checksum")][0]
    print("  app.tgz md5:", md5, "manifest:", checksum, "OK" if md5 == checksum else "MISMATCH")
    with tarfile.open(fileobj=io.BytesIO(app_tgz), mode="r:gz") as inner:
        inner_names = inner.getnames()
        print("  app files:", ", ".join(inner_names))
        assert "ui/config" in inner_names, "missing ui/config"
        ui_config = inner.extractfile("ui/config").read().decode("utf-8")
        assert '"gatewayPrefix": "/app/fnrelay"' in ui_config, "missing gateway entry"
        assert '"type": "iframe"' in ui_config, "entry is not iframe"
        bin_data = inner.extractfile("server/fnrelay").read(64)
    print("  binary magic:", bin_data[:4].hex(), "machine:", hex(int.from_bytes(bin_data[18:20], "little")))
    if int.from_bytes(bin_data[18:20], "little") != machine:
        print("  !! wrong ELF machine")
    assert b"< /dev/null" in main, "missing stdin redirect"
    assert b"\r\n" not in main, "CRLF in cmd/main"
    assert b'"run-as": "root"' in priv
    assert "u.config" not in text
    for need in ("ICON.PNG", "ICON_256.PNG", "cmd/main", "config/resource", "app.tgz"):
        assert need in names, need
    print("  layout OK")


check("x86", 0x3E)
check("arm", 0xB7)

with open(os.path.join(DIST, "SHA256SUMS.txt"), "w", encoding="utf-8") as f:
    for name in ("FnRelay-1.0.24-x86.fpk", "FnRelay-1.0.24-arm.fpk", "FnRelay-1.0.24-source.zip"):
        path = os.path.join(DIST, name)
        if not os.path.exists(path):
            continue
        h = hashlib.sha256(open(path, "rb").read()).hexdigest()
        f.write("%s  %s\n" % (h, name))
        print(name, h)
print("done")
