import json
import urllib.request

BASE = "http://127.0.0.1:18899/app/fnrelay"

r = urllib.request.urlopen(BASE + "/", timeout=8)
html = r.read().decode("utf-8")
print("index", r.status, "base-tag", '<base href="/app/fnrelay/">' in html, "relative-js", "assets/app.js" in html)

r = urllib.request.urlopen(BASE + "/assets/style.css", timeout=8)
css = r.read().decode("utf-8")
print("css", r.status, len(css), "light-theme", 'data-theme="light"' in css)

theme = urllib.request.urlopen(BASE + "/assets/theme.js", timeout=8)
print("theme.js", theme.status, theme.read().decode("utf-8")[:60].replace("\n", " "))

state = json.load(urllib.request.urlopen(BASE + "/api/state", timeout=8))
print("state", state["authenticated"], state["version"], state["status"]["proxy_running"], len(state["config"]["rules"]))
