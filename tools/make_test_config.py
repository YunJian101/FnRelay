import json
import os

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DATA = os.path.join(ROOT, "testdata")
CERTDIR = os.path.join(DATA, "certs")

cfg = {
    "version": 1,
    "admin": {"password_hash": "", "session_days": 7},
    "proxy": {
        "port": 18443,
        "http_redirect": True,
        "redirect_mode": "same_port",
        "redirect_port": 18443,
        "extra_http_port": 18080,
        "default_upstream": "",
        "hsts": False,
        "auto_fallback": False,
        "title": "FnRelay",
    },
    "rules": [
        {"id": "r-exact", "name": "app", "enabled": True, "domains": ["app.test.local"],
         "upstream": "http://127.0.0.1:18081", "preserve_host": True},
        {"id": "r-wild", "name": "wild", "enabled": True, "domains": ["*.test.local"],
         "upstream": "http://127.0.0.1:18081", "preserve_host": True},
    ],
    "certs": {"auto_fnos": False, "extra_dirs": [CERTDIR], "default_cert": ""},
    "logs": {"file_enabled": False, "access": True, "audit": True, "retention_days": 7, "max_file_mb": 100},
}

os.makedirs(DATA, exist_ok=True)
with open(os.path.join(DATA, "config.json"), "w", encoding="utf-8") as f:
    json.dump(cfg, f, ensure_ascii=False, indent=2)
print("test config written")
