"""多线程分片下载 GitHub Release 资产（绕开单连接带宽上限）。

用法：
  python tools/gh_download.py <tag> <文件名匹配> [--out DIR] [--threads 8]

产物：<out>/<原文件名>
"""
import argparse
import os
import re
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request

ROOT = r"E:\6\guoapp"
OWNER, REPO = "serra006", "guoapp"

# 通道优先级：环境变量里配的代理（系统/终端代理，端口会变） -> 直连 -> 20808（v2rayN 老端口）
_ENV_PROXY = os.environ.get("HTTPS_PROXY") or os.environ.get("https_proxy") or ""
PROXIES = [p for p in (_ENV_PROXY, None, "http://127.0.0.1:20808") if p]


def token():
    url = subprocess.run(["git", "remote", "get-url", "origin"], cwd=ROOT,
                         capture_output=True, text=True, check=True).stdout.strip()
    return re.search(r"https://[^:]+:([^@]+)@", url).group(1)


TOKEN = token()


def opener(proxy):
    h = [urllib.request.ProxyHandler({} if proxy is None else {"http": proxy, "https": proxy})]
    return urllib.request.build_opener(*h)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *a, **k):
        return None


def api_json(path):
    last = None
    for p in PROXIES:
        req = urllib.request.Request("https://api.github.com" + path)
        req.add_header("Authorization", "Bearer " + TOKEN)
        req.add_header("User-Agent", "curl/8.5.0")
        try:
            with opener(p).open(req, timeout=45) as r:
                return __import__("json").loads(r.read().decode())
        except urllib.error.HTTPError as exc:
            raise SystemExit(f"HTTP {exc.code}: {exc.read().decode(errors='replace')[:300]}")
        except Exception as exc:  # noqa: BLE001
            last = exc
    raise SystemExit(f"API 不通: {last}")


def resolve(asset_id):
    """302 -> 真实直链（objects.githubusercontent.com，SAS 自带签名，别带 Authorization）。"""
    for p in PROXIES:
        req = urllib.request.Request(
            f"https://api.github.com/repos/{OWNER}/{REPO}/releases/assets/{asset_id}")
        req.add_header("Authorization", "Bearer " + TOKEN)
        req.add_header("Accept", "application/octet-stream")
        req.add_header("User-Agent", "curl/8.5.0")
        try:
            op = urllib.request.build_opener(
                urllib.request.ProxyHandler({} if p is None else {"http": p, "https": p}),
                NoRedirect)
            with op.open(req, timeout=45) as r:
                loc = r.headers.get("Location")
        except urllib.error.HTTPError as exc:
            loc = exc.headers.get("Location") if exc.code in (301, 302, 303, 307) else None
        except Exception:  # noqa: BLE001
            continue
        if loc:
            return loc
    raise SystemExit("拿不到直链")


def probe(url):
    """返回 (总长度, 是否支持 Range)。"""
    for p in PROXIES:
        req = urllib.request.Request(url, headers={"User-Agent": "curl/8.5.0", "Range": "bytes=0-0"})
        try:
            with opener(p).open(req, timeout=30) as r:
                cr = r.headers.get("Content-Range") or ""
                m = re.search(r"/(\d+)", cr)
                total = int(m.group(1)) if m else int(r.headers.get("Content-Length") or 0)
                return total, bool(m), p
        except Exception:  # noqa: BLE001
            continue
    raise SystemExit("探测失败")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("tag")
    ap.add_argument("match")
    ap.add_argument("--out", default=r"E:\6\apk77")
    ap.add_argument("--threads", type=int, default=8)
    args = ap.parse_args()

    rel = api_json(f"/repos/{OWNER}/{REPO}/releases/tags/{args.tag}")
    assets = [a for a in rel["assets"] if args.match in a["name"]]
    if not assets:
        raise SystemExit(f"{args.tag} 里没有匹配 {args.match} 的资产")
    asset = assets[0]
    print(f'资产 {asset["name"]} {asset["size"]}B')

    url = resolve(asset["id"])
    print(f'直链域名 {url.split("/")[2]}')
    total, ranged, proxy = probe(url)
    print(f"长度 {total}B  Range={ranged}  通道={proxy}")
    if total <= 0:
        raise SystemExit("长度未知")

    os.makedirs(args.out, exist_ok=True)
    dest = os.path.join(args.out, asset["name"])
    part = dest + ".part"
    if os.path.exists(part):
        os.remove(part)  # 不复用：truncate 预分配过，大小不等于完成度
    open(part, "wb").close()
    n = args.threads if ranged else 1
    chunk = total // n
    segs = []
    for i in range(n):
        s = i * chunk
        e = total - 1 if i == n - 1 else (i + 1) * chunk - 1
        segs.append([s, e, 0])

    lock = threading.Lock()
    done = [0]
    err = []

    def worker(idx):
        s, e, _ = segs[idx]
        pos = s
        tries = 0
        while pos <= e:
            if tries >= 4:
                return
            try:
                req = urllib.request.Request(
                    url, headers={"User-Agent": "curl/8.5.0", "Range": f"bytes={pos}-{e}"})
                with opener(proxy).open(req, timeout=45) as r:
                    fh = open(part, "r+b")
                    try:
                        fh.seek(pos)
                        while pos <= e:
                            b = r.read(262144)
                            if not b:
                                break
                            fh.write(b)
                            pos += len(b)
                            with lock:
                                done[0] += len(b)
                    finally:
                        fh.close()
            except Exception as exc:  # noqa: BLE001
                tries += 1
                err.append(f"seg{idx}:{type(exc).__name__}")
                if pos > e:
                    return
                time.sleep(0.5)

    threads = [threading.Thread(target=worker, args=(i,), daemon=True) for i in range(n)]
    t0 = time.time()
    for t in threads:
        t.start()
    while any(t.is_alive() for t in threads):
        done_n = done[0]
        el = max(time.time() - t0, 0.001)
        print(f"\r  {done_n * 100 // total:3d}%  {done_n // 1024}KB  "
              f"{done_n / el / 1024:.0f}KB/s", end="", flush=True)
        if done_n >= total:
            break
        time.sleep(0.5)

    for t in threads:
        t.join(timeout=3)
    print()
    size = os.path.getsize(part)
    if size != total:
        raise SystemExit(f"不完整 {size}/{total}  错误 {err[:4]}")
    os.replace(part, dest)
    print(f"OK {dest} {os.path.getsize(dest)}B  用时 {time.time() - t0:.0f}s")


if __name__ == "__main__":
    main()
