"""从最新一次成功的 CI 运行里下载 TV flavor 的 APK。

github.com 的 git 端口被阻断，但 api.github.com 可用；artifact 的 zip 会 302 到
blob.core.windows.net，这里用同一个代理跟随重定向下载。
"""
import json
import os
import re
import subprocess
import sys
import urllib.request
import urllib.error
import zipfile

ROOT = r"E:\6\guoapp"
OUT = r"E:\6\apk75"
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
direct = urllib.request.build_opener(urllib.request.ProxyHandler({}))


def api(path):
    req = urllib.request.Request("https://api.github.com" + path)
    req.add_header("Authorization", "Bearer " + TOKEN)
    req.add_header("Accept", "application/vnd.github+json")
    req.add_header("User-Agent", "curl/8.5.0")
    with opener.open(req, timeout=60) as r:
        return json.loads(r.read().decode())


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


noauth_proxy = urllib.request.build_opener(
    urllib.request.ProxyHandler({"http": PROXY, "https": PROXY})
)
noauth_direct = urllib.request.build_opener(urllib.request.ProxyHandler({}))
no_redirect = urllib.request.build_opener(
    urllib.request.ProxyHandler({"http": PROXY, "https": PROXY}), NoRedirect
)


def resolve_location(art_id):
    """先拿到 302 的 SAS 地址。Azure blob 不接受带 Bearer 的请求，
    必须把 Authorization 去掉后再下——否则回 401 AuthenticationFailed。"""
    req = urllib.request.Request(
        f"https://api.github.com/repos/{OWNER}/{REPO}/actions/artifacts/{art_id}/zip"
    )
    req.add_header("Authorization", "Bearer " + TOKEN)
    req.add_header("User-Agent", "curl/8.5.0")
    try:
        with no_redirect.open(req, timeout=60) as r:
            loc = r.headers.get("Location")
    except urllib.error.HTTPError as exc:
        loc = exc.headers.get("Location") if exc.code in (301, 302, 303, 307) else None
    if not loc:
        raise RuntimeError("没有拿到重定向地址")
    print(f"  SAS 域名: {loc.split('/')[2]}")
    return loc


def download(art_id, dest):
    loc = resolve_location(art_id)
    for name, op in (("proxy", noauth_proxy), ("direct", noauth_direct)):
        try:
            req = urllib.request.Request(loc)
            req.add_header("User-Agent", "curl/8.5.0")
            with op.open(req, timeout=240) as r, open(dest, "wb") as f:
                total = int(r.headers.get("Content-Length") or 0)
                done = 0
                while True:
                    chunk = r.read(262144)
                    if not chunk:
                        break
                    f.write(chunk)
                    done += len(chunk)
                    if total:
                        print(f"\r  {name} {done * 100 // total}%", end="", flush=True)
            print(f"\r  {name} 下载完成 {os.path.getsize(dest)}B")
            return True
        except Exception as exc:  # noqa: BLE001
            print(f"\n  {name} 失败: {type(exc).__name__}: {exc}")
    return False


def main():
    run = api(f"/repos/{OWNER}/{REPO}/actions/runs?per_page=6")["workflow_runs"]
    run = next((r for r in run if r["conclusion"] == "success"), None)
    if not run:
        raise SystemExit("最近 6 次都没有成功的运行")
    print(f'用 #{run["run_number"]} {run["head_sha"][:8]}')

    arts = api(f'/repos/{OWNER}/{REPO}/actions/runs/{run["id"]}/artifacts')["artifacts"]
    if not arts:
        raise SystemExit("该运行没有 artifact")
    for a in arts:
        print(f'  artifact {a["name"]} {a["size_in_bytes"]}B id={a["id"]}')

    tv = [a for a in arts if "tv" in a["name"].lower()]
    if not tv:
        raise SystemExit("没找到 tv flavor 的 artifact")
    tv.sort(key=lambda a: a["size_in_bytes"])
    target = tv[-1]
    print(f'选 {target["name"]}')

    os.makedirs(OUT, exist_ok=True)
    zpath = os.path.join(OUT, "tv.zip")
    if not download(target["id"], zpath):
        raise SystemExit("下载失败")

    with zipfile.ZipFile(zpath) as z:
        apks = [n for n in z.namelist() if n.lower().endswith(".apk")]
        print("  压缩包内:", apks)
        z.extractall(os.path.join(OUT, "_x"))
    for n in apks:
        src = os.path.join(OUT, "_x", n)
        dst = os.path.join(OUT, os.path.basename(n))
        os.replace(src, dst)
        print(f"  -> {dst}  {os.path.getsize(dst)}B")
    print(f"OUT_DIR={OUT}")


if __name__ == "__main__":
    main()
