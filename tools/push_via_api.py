"""通过 GitHub Git Data API 推送本地改动（绕过 github.com 的 TLS 阻断，走 api.github.com）。

本机 `git push` 到 github.com 常被 RST / TLS 中断，但 api.github.com 通常可用，
因此用 REST API 手工构造 blob → tree → commit → ref 更新。

用法：
    python tools/push_via_api.py                        # 推送工作区未提交的改动
    python tools/push_via_api.py a.md b.png             # 只推指定路径（补推已提交文件）
    python tools/push_via_api.py --message="docs: xxx"  # 覆盖 commit message
    python tools/push_via_api.py --repo                    # 顺手改仓库简介与 topics
"""
import base64
import os
import subprocess
import sys

import gh_api as api

BRANCH = "main"

# 仓库简介与话题：改这里再跑 `--repo` 即可同步到 GitHub。
DESCRIPTION = (
    "真果TV · Android TV 短剧应用：10-foot 大屏重制界面，遥控器全流程，"
    "焦点三重可辨；内置 12 个站源，解析与播放全部本地完成"
)
TOPICS = [
    "android-tv",
    "flutter",
    "dart",
    "leanback",
    "tv",
    "mpv",
    "media-kit",
    "short-drama",
    "golang",
    "offline",
]


def git(*args):
    return subprocess.run(
        ["git", *args], cwd=api.ROOT, capture_output=True, text=True, check=True
    ).stdout.strip()


def changed_files():
    """用工作区未提交改动作为推送集合。

    远程 commit 是 API 造的，本地没有该对象，`git diff <远程sha> HEAD` 会失败，
    所以改成读 `git status -z`：只处理当前未提交/未跟踪的文件。
    注意必须用 -z（NUL 分隔）——普通模式下首行的前导空格会被 stdout.strip() 吃掉，
    导致路径少一个字符（踩过：TV-CHANGES.md 变成 V-CHANGES.md）。
    """
    out = subprocess.run(
        ["git", "status", "--porcelain", "-z", "-uall"],
        cwd=api.ROOT,
        capture_output=True,
        text=True,
        check=True,
    ).stdout
    rows = []
    for item in out.split("\0"):
        if len(item) < 4:
            continue
        status = item[:2].strip()
        path = item[3:]
        if status.startswith("D"):
            rows.append(("D", path))
        else:
            rows.append(("A" if status in ("??", "A") else "M", path))
    return rows


def set_repo_meta():
    """更新仓库简介与话题。需要 token 具备 Administration: write。"""
    result = api.patch(api.repo_path(), {"description": DESCRIPTION, "topics": TOPICS})
    print(f"简介  = {result.get('description')}")
    print(f"topics = {', '.join(result.get('topics') or [])}")


def main():
    paths = [a for a in sys.argv[1:] if not a.startswith("--")]
    flags = [a for a in sys.argv[1:] if a.startswith("--")]
    msg_override = next(
        (a.split("=", 1)[1] for a in flags if a.startswith("--message=")), None
    )

    if "--repo" in flags:
        set_repo_meta()
        if not paths:
            return

    rows = [("A", p) for p in paths] if paths else changed_files()
    if not rows:
        print("没有差异，退出")
        return

    remote_ref = api.get(api.repo_path(f"/git/ref/heads/{BRANCH}"))
    base_sha = remote_ref["object"]["sha"]
    base_tree = api.get(api.repo_path(f"/git/commits/{base_sha}"))["tree"]["sha"]
    print(f"远程 HEAD = {base_sha[:8]}  tree = {base_tree[:8]}")
    print(f"待推送文件 {len(rows)} 个  [transport={api._state['name']}]")

    entries = []
    for status, path in rows:
        if status == "D":
            print(f"  D {path}")
            entries.append({"path": path, "mode": "100644", "type": "blob", "sha": None})
            continue
        full = os.path.join(api.ROOT, path.replace("/", os.sep))
        if not os.path.isfile(full):
            print(f"  跳过（本地缺失）{path}")
            continue
        with open(full, "rb") as f:
            raw = f.read()
        # 二进制（png/jpg）走同一套 base64 blob 接口，体积 >1MB 也照样推。
        blob = api.post(
            api.repo_path("/git/blobs"),
            {"content": base64.b64encode(raw).decode(), "encoding": "base64"},
        )
        print(f"  {status} {path}  {len(raw)}B -> {blob['sha'][:8]}")
        entries.append(
            {"path": path, "mode": "100644", "type": "blob", "sha": blob["sha"]}
        )

    tree = api.post(
        api.repo_path("/git/trees"), {"base_tree": base_tree, "tree": entries}
    )
    print(f"新 tree = {tree['sha'][:8]}")

    message = msg_override or git("log", "-1", "--pretty=%B", "HEAD")
    commit = api.post(
        api.repo_path("/git/commits"),
        {"message": message, "tree": tree["sha"], "parents": [base_sha]},
    )
    print(f"新 commit = {commit['sha'][:8]}")

    api.patch(
        api.repo_path(f"/git/refs/heads/{BRANCH}"),
        {"sha": commit["sha"], "force": False},
    )
    print("分支已更新")

    # 本地没有这个 commit 对象（是 API 造的），update-ref 会失败——忽略即可。
    # 网络恢复后 `git fetch origin` 会把这些对象拉下来并对齐。
    try:
        git("update-ref", f"refs/remotes/origin/{BRANCH}", commit["sha"])
    except subprocess.CalledProcessError:
        print("（本地缺少远程 commit 对象，跳过 origin 引用更新）")
    print(f'REMOTE_HEAD={commit["sha"]}')


if __name__ == "__main__":
    main()
