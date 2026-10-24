"""轮询最新一次 CI 运行直到结束，打印结论与失败步骤。"""
import json
import re
import subprocess
import sys
import time
import urllib.request

ROOT = r"E:\6\guoapp"
PROXY = "http://127.0.0.1:58366"
OWNER, REPO = "serra006", "guoapp"

url = subprocess.run(
    ["git", "remote", "get-url", "origin"],
    cwd=ROOT,
    capture_output=True,
    text=True,
    check=True,
).stdout.strip()
TOKEN = re.search(r"https://[^:]+:([^@]+)@", url).group(1)

opener = urllib.request.build_opener(
    urllib.request.ProxyHandler({"http": PROXY, "https": PROXY})
)


def get(path):
    req = urllib.request.Request("https://api.github.com" + path)
    req.add_header("Authorization", "Bearer " + TOKEN)
    req.add_header("Accept", "application/vnd.github+json")
    req.add_header("User-Agent", "curl/8.5.0")
    with opener.open(req, timeout=60) as r:
        return json.loads(r.read().decode())


run = get(f"/repos/{OWNER}/{REPO}/actions/runs?per_page=1")["workflow_runs"][0]
rid = run["id"]
print(f'跟踪 #{run["run_number"]} {run["head_sha"][:8]}', flush=True)

deadline = time.time() + 60 * 45
last = None
while time.time() < deadline:
    run = get(f"/repos/{OWNER}/{REPO}/actions/runs/{rid}")
    state = f'{run["status"]}/{run["conclusion"]}'
    if state != last:
        print(state, flush=True)
        last = state
    if run["status"] == "completed":
        break
    time.sleep(30)

for job in get(f"/repos/{OWNER}/{REPO}/actions/runs/{rid}/jobs")["jobs"]:
    print(f'\njob {job["name"]}: {job["status"]}/{job["conclusion"]}')
    for step in job["steps"] or []:
        mark = "x" if step["conclusion"] not in (None, "success", "skipped") else " "
        print(f'  [{mark}] {step["name"]}: {step["conclusion"]}')

arts = get(f"/repos/{OWNER}/{REPO}/actions/runs/{rid}/artifacts")["artifacts"]
print("\nartifacts:")
for a in arts:
    print(f'  {a["name"]}  {a["size_in_bytes"]}B  id={a["id"]}')
