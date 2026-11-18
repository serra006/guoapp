import sys, os
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import gh_api as api

for repo in ("zhoufuweigg/guoapp",):
    try:
        r = api.get(f"/repos/{repo}")
        print(repo, "存在:", r.get("full_name"), "archived=", r.get("archived"), "disabled=", r.get("disabled"))
    except Exception as e:
        print(repo, "取不到 ->", str(e)[:160])

try:
    r = api.get(api.repo_path())
    print("ours: fork=", r.get("fork"), "parent=", (r.get("parent") or {}).get("full_name"), "source=", (r.get("source") or {}).get("full_name"))
except Exception as e:
    print("ours err", e)
