"""从最新一次成功的 CI 运行里下载 APK（tv / 手机端两个 flavor 都下）。

github.com 的 git 端口被阻断，但 api.github.com 可用；artifact 的 zip 会 302 到
blob.core.windows.net，必须去掉 Authorization 头再下（SAS 自带签名）。

通道顺序：直连 -> 20808（v2rayN）。58366 是 WorkBuddy 内置代理，随会话失效，不再使用。

用法：
  python tools/fetch_apk.py                 # 下到 E:/6/apk77
  python tools/fetch_apk.py --out E:/6/xx   # 指定目录
  python tools/fetch_apk.py --only tv       # 只下电视端
"""
import argparse
import json
import os
import re
import subprocess
import urllib.error
import urllib.request
import zipfile

ROOT = r"E:\6\guoapp"
OWNER, REPO = "serra006", "guoapp"

# None 表示直连；后面是可回退的本地代理
CHANNELS = [("direct", None), ("20808", "http://127.0.0.1:20808")]


def make_opener(proxy, no_redirect=False):
    handlers = [urllib.request.ProxyHandler({} if proxy is None else {"http": proxy, "https": proxy})]
    if no_redirect:
        handlers.append(NoRedirect)
    return urllib.request.build_opener(*handlers)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def build_openers():
    return [(name, make_opener(p), make_opener(p, True)) for name, p in CHANNELS]


def get_token():
    url = subprocess.run(
        ["git", "remote", "get-url", "origin"],
        cwd=ROOT, capture_output=True, text=True, check=True,
    ).stdout.strip()
    m = re.search(r"https://[^:]+:([^@]+)@", url)
    if not m:
        raise SystemExit("origin 里没有 token，先检查 git remote")
    return m.group(1)


TOKEN = get_token()
OPENERS = build_openers()


def api(path):
    last = None
    for name, op, _ in OPENERS:
        req = urllib.request.Request("https://api.github.com" + path)
        req.add_header("Authorization", "Bearer " + TOKEN)
        req.add_header("Accept", "application/vnd.github+json")
        req.add_header("User-Agent", "curl/8.5.0")
        try:
            with op.open(req, timeout=60) as r:
                if name != "direct":
                    print(f"  [通道 {name}]")
                return json.loads(r.read().decode())
        except urllib.error.HTTPError as exc:
            body = exc.read().decode(errors="replace")[:300]
            raise SystemExit(f"HTTP {exc.code} on {path}: {body}")
        except Exception as exc:  # noqa: BLE001
            last = f"{name}: {type(exc).__name__}: {exc}"
            continue
    raise SystemExit(f"所有通道都失败（最后一次 {last}）")


def resolve_location(art_id):
    """拿 302 的 SAS 地址。"""
    last = None
    for name, _, no_redir in OPENERS:
        req = urllib.request.Request(
            f"https://api.github.com/repos/{OWNER}/{REPO}/actions/artifacts/{art_id}/zip"
        )
        req.add_header("Authorization", "Bearer " + TOKEN)
        req.add_header("User-Agent", "curl/8.5.0")
        try:
            with no_redir.open(req, timeout=60) as r:
                loc = r.headers.get("Location")
        except urllib.error.HTTPError as exc:
            loc = exc.headers.get("Location") if exc.code in (301, 302, 303, 307) else None
        except Exception as exc:  # noqa: BLE001
            last = f"{name}: {type(exc).__name__}: {exc}"
            continue
        if loc:
            print(f"  SAS 域名: {loc.split('/')[2]}")
            return loc
        last = f"{name}: 无 Location"
    raise RuntimeError(f"没有拿到重定向地址（{last}）")


def download(art_id, dest):
    loc = resolve_location(art_id)
    for name, op, _ in OPENERS:
        try:
            req = urllib.request.Request(loc)
            req.add_header("User-Agent", "curl/8.5.0")
            with op.open(req, timeout=300) as r, open(dest, "wb") as f:
                total = int(r.headers.get("Content-Length") or 0)
                done = 0
                while True:
                    chunk = r.read(262144)
                    if not chunk:
                        break
                    f.write(chunk)
                    done += len(chunk)
            print(f"  [{name}] 下载完成 {os.path.getsize(dest)}B")
            return True
        except Exception as exc:  # noqa: BLE001
            print(f"  [{name}] 失败: {type(exc).__name__}: {exc}")
    return False


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", default=r"E:\6\apk77")
    ap.add_argument("--only", choices=["tv", "phone"], default=None)
    args = ap.parse_args()

    runs = api(f"/repos/{OWNER}/{REPO}/actions/runs?per_page=8")["workflow_runs"]
    run = next((r for r in runs if r["conclusion"] == "success"), None)
    if not run:
        raise SystemExit("最近 8 次都没有成功的运行")
    print(f'用 #{run["run_number"]} {run["head_sha"][:8]} ({run["created_at"]})')

    arts = api(f'/repos/{OWNER}/{REPO}/actions/runs/{run["id"]}/artifacts')["artifacts"]
    if not arts:
        raise SystemExit("该运行没有 artifact")
    for a in arts:
        print(f'  artifact {a["name"]} {a["size_in_bytes"]}B id={a["id"]}')

    os.makedirs(args.out, exist_ok=True)
    wanted = []
    for a in arts:
        low = a["name"].lower()
        if "tv" in low:
            wanted.append(("tv", a))
        elif "android" in low:
            wanted.append(("phone", a))

    if args.only == "tv":
        wanted = [w for w in wanted if w[0] == "tv"]
    elif args.only == "phone":
        wanted = [w for w in wanted if w[0] == "phone"]
    if not wanted:
        raise SystemExit("没找到匹配的 artifact")

    ok = []
    for kind, a in wanted:
        print(f'\n[{kind}] {a["name"]}')
        zpath = os.path.join(args.out, f"{kind}.zip")
        if not download(a["id"], zpath):
            print(f"  {kind} 下载失败，跳过")
            continue
        with zipfile.ZipFile(zpath) as z:
            apks = [n for n in z.namelist() if n.lower().endswith(".apk")]
            print("  压缩包内:", apks)
            z.extractall(os.path.join(args.out, "_x"))
        for n in apks:
            src = os.path.join(args.out, "_x", n)
            dst = os.path.join(args.out, os.path.basename(n))
            os.replace(src, dst)
            ok.append(dst)
            print(f"  -> {dst}  {os.path.getsize(dst)}B")

    print(f"\nOUT_DIR={args.out}")
    for p in ok:
        print("  " + p)


if __name__ == "__main__":
    main()
