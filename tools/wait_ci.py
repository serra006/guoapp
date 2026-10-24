"""轮询最新一次 CI 运行直到结束，打印结论与失败步骤。"""
import sys
import time

import gh_api as api

run = api.get(api.repo_path("/actions/runs?per_page=1"))["workflow_runs"][0]
rid = run["id"]
print(f'跟踪 #{run["run_number"]} {run["head_sha"][:8]}', flush=True)

deadline = time.time() + 60 * 45
last = None
while time.time() < deadline:
    run = api.get(api.repo_path(f"/actions/runs/{rid}"))
    state = f'{run["status"]}/{run["conclusion"]}'
    if state != last:
        print(state, flush=True)
        last = state
    if run["status"] == "completed":
        break
    time.sleep(30)

failed = 0
for job in api.get(api.repo_path(f"/actions/runs/{rid}/jobs"))["jobs"]:
    print(f'\njob {job["name"]}: {job["status"]}/{job["conclusion"]}')
    for step in job["steps"] or []:
        mark = "x" if step["conclusion"] not in (None, "success", "skipped") else " "
        if mark == "x":
            failed += 1
        print(f'  [{mark}] {step["name"]}: {step["conclusion"]}')

arts = api.get(api.repo_path(f"/actions/runs/{rid}/artifacts"))["artifacts"]
print("\nartifacts:")
for a in arts:
    print(f'  {a["name"]}  {a["size_in_bytes"]}B  id={a["id"]}')

sys.exit(1 if failed else 0)
