"""查询最近若干次 CI 运行状态（走 api.github.com，绕开 github.com 阻断）。"""
import sys

import gh_api as api

n = int(sys.argv[1]) if len(sys.argv) > 1 else 3
runs = api.get(api.repo_path(f"/actions/runs?per_page={n}"))["workflow_runs"]
for run in runs:
    print(
        f'#{run["run_number"]} {run["status"]}/{run["conclusion"]} '
        f'{run["head_sha"][:8]} {run["created_at"]} '
        f'{run["display_title"][:40]}'
    )
    print(f'  {run["html_url"]}')
    if run["status"] != "completed":
        jobs = api.get(api.repo_path(f'/actions/runs/{run["id"]}/jobs'))["jobs"]
        for job in jobs:
            print(f'  job: {job["name"]} {job["status"]}/{job["conclusion"]}')
            for step in job["steps"] or []:
                if step["conclusion"] not in (None, "success", "skipped"):
                    print(f'     x {step["name"]} -> {step["conclusion"]}')
