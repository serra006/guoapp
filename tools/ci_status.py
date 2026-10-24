"""查询最近若干次 CI 运行状态（走 api.github.com，绕开 github.com 阻断）。"""
import json
import re
import subprocess
import sys
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


n = int(sys.argv[1]) if len(sys.argv) > 1 else 3
for run in get(f"/repos/{OWNER}/{REPO}/actions/runs?per_page={n}")["workflow_runs"]:
    print(
        f'#{run["run_number"]} {run["status"]}/{run["conclusion"]} '
        f'{run["head_sha"][:8]} {run["created_at"]} '
        f'{run["display_title"][:40]}'
    )
    print(f'  {run["html_url"]}')
    if run["status"] != "completed":
        for job in get(f'/repos/{OWNER}/{REPO}/actions/runs/{run["id"]}/jobs')["jobs"]:
            print(f'  job: {job["name"]} {job["status"]}/{job["conclusion"]}')
            for step in job["steps"] or []:
                if step["conclusion"] not in (None, "success", "skipped"):
                    print(f'     x {step["name"]} -> {step["conclusion"]}')
