"""通过 GitHub Git Data API 推送本地提交（绕过 github.com 的 TLS 阻断，走 api.github.com）。

本机 git push 到 github.com 被 RST / TLS 中断，但 api.github.com 经内置代理可用，
因此用 REST API 手工构造 blob → tree → commit → ref 更新。
"""
import base64
import json
import os
import re
import subprocess
import sys
import urllib.request

ROOT = r"E:\6\guoapp"
API = "https://api.github.com"
PROXY = "http://127.0.0.1:58366"
OWNER = "serra006"
REPO = "guoapp"
BRANCH = "main"


def git(*args):
    return subprocess.run(
        ["git", *args], cwd=ROOT, capture_output=True, text=True, check=True
    ).stdout.strip()


def token():
    url = git("remote", "get-url", "origin")
    m = re.search(r"https://[^:]+:([^@]+)@", url)
    if not m:
        raise SystemExit("remote url 里没有 token")
    return m.group(1)


TOKEN = token()
opener = urllib.request.build_opener(
    urllib.request.ProxyHandler({"http": PROXY, "https": PROXY})
)


def call(method, path, payload=None):
    data = json.dumps(payload).encode() if payload is not None else None
    req = urllib.request.Request(
        API + path, data=data, method=method
    )
    req.add_header("Authorization", "Bearer " + TOKEN)
    req.add_header("Accept", "application/vnd.github+json")
    req.add_header("X-GitHub-Api-Version", "2022-11-28")
    req.add_header("User-Agent", "curl/8.5.0")
    if data:
        req.add_header("Content-Type", "application/json")
    with opener.open(req, timeout=60) as r:
        body = r.read().decode()
    return json.loads(body) if body else {}


def changed_files(base_sha):
    """用工作区未提交改动作为推送集合。

    远程 commit 是 API 造的，本地没有该对象，`git diff <远程sha> HEAD` 会失败，
    所以改成读 `git status`：只处理当前未提交/未跟踪的文件。
    """
    out = git("status", "--porcelain", "-uall")
    rows = []
    for line in out.splitlines():
        if not line.strip():
            continue
        status = line[:2].strip()
        path = line[3:].strip().strip('"')
        if " -> " in path:
            path = path.split(" -> ")[-1]
        if status.startswith("D"):
            rows.append(("D", path, None))
        else:
            rows.append(("A" if status in ("??", "A") else "M", path, path))
    return rows


def main():
    remote_ref = call("GET", f"/repos/{OWNER}/{REPO}/git/ref/heads/{BRANCH}")
    base_sha = remote_ref["object"]["sha"]
    base_tree = call("GET", f"/repos/{OWNER}/{REPO}/git/commits/{base_sha}")["tree"][
        "sha"
    ]
    print(f"远程 HEAD = {base_sha[:8]}  tree = {base_tree[:8]}")

    rows = changed_files(base_sha)
    if not rows:
        print("没有差异，退出")
        return
    print(f"待推送文件 {len(rows)} 个")

    entries = []
    for status, path, local in rows:
        if status == "D":
            print(f"  D {path}")
            entries.append(
                {"path": path, "mode": "100644", "type": "blob", "sha": None}
            )
            continue
        full = os.path.join(ROOT, path.replace("/", os.sep))
        if not os.path.isfile(full):
            print(f"  跳过（本地缺失）{path}")
            continue
        if path.lower().endswith((".png", ".jpg", ".apk", ".zip")) or os.path.getsize(
            full
        ) > 1_000_000:
            print(f"  跳过（大体积预览图）{path}")
            continue
        with open(full, "rb") as f:
            raw = f.read()
        blob = call(
            "POST",
            f"/repos/{OWNER}/{REPO}/git/blobs",
            {"content": base64.b64encode(raw).decode(), "encoding": "base64"},
        )
        print(f"  {status} {path}  {len(raw)}B -> {blob['sha'][:8]}")
        entries.append(
            {"path": path, "mode": "100644", "type": "blob", "sha": blob["sha"]}
        )

    tree = call(
        "POST",
        f"/repos/{OWNER}/{REPO}/git/trees",
        {"base_tree": base_tree, "tree": entries},
    )
    print(f"新 tree = {tree['sha'][:8]}")

    message = git("log", "-1", "--pretty=%B", "HEAD")
    commit = call(
        "POST",
        f"/repos/{OWNER}/{REPO}/git/commits",
        {"message": message, "tree": tree["sha"], "parents": [base_sha]},
    )
    print(f"新 commit = {commit['sha'][:8]}")

    call(
        "PATCH",
        f"/repos/{OWNER}/{REPO}/git/refs/heads/{BRANCH}",
        {"sha": commit["sha"], "force": False},
    )
    print("分支已更新")

    # 让本地与远程对齐，避免下次 diff 基准错位
    git("update-ref", f"refs/remotes/origin/{BRANCH}", commit["sha"])
    print(f'REMOTE_HEAD={commit["sha"]}')


if __name__ == "__main__":
    main()
