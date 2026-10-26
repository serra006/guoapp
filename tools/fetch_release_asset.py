"""断点续传下载 GitHub Release 资产（本机代理限速时会频繁掐断连接）。

用法: python tools/fetch_release_asset.py <tag> <asset-name> [<out-dir>]
要点:
  - 先 302 拿 Azure SAS URL，跟随重定向时**必须去掉 Authorization 头**（否则 401）；
  - 按已下载字节数发 Range 请求续传，最多重试 40 次；
  - 单次 read 超时 45s，超时/断流都算可重试错误。
"""
import os
import sys
import time
import urllib.error
import urllib.request

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import gh_api  # noqa: E402

REPO = "serra006/guoapp"


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def _asset(tag: str, name: str) -> dict:
    release = gh_api.get(f"/repos/{REPO}/releases/tags/{tag}")
    for asset in release.get("assets", []):
        if asset["name"] == name:
            return asset
    raise SystemExit(f"{tag} 里没有 {name}")


def _sas_url(asset_id: int) -> str:
    url = f"https://api.github.com/repos/{REPO}/releases/assets/{asset_id}"
    req = urllib.request.Request(
        url,
        headers={
            "Authorization": f"Bearer {gh_api.token()}",
            "Accept": "application/octet-stream",
        },
    )
    try:
        urllib.request.build_opener(_NoRedirect).open(req, timeout=30)
    except urllib.error.HTTPError as exc:
        location = exc.headers.get("Location")
        if exc.code in (301, 302, 303, 307) and location:
            return location
        raise SystemExit(f"取下载地址失败: HTTP {exc.code}")
    raise SystemExit("没有拿到重定向地址")


def download(tag: str, name: str, out_dir: str) -> str:
    os.makedirs(out_dir, exist_ok=True)
    target = os.path.join(out_dir, name)
    asset = _asset(tag, name)
    total = int(asset["size"])
    tries = 0
    while True:
        have = os.path.getsize(target) if os.path.exists(target) else 0
        if have >= total:
            break
        tries += 1
        if tries > 40:
            raise SystemExit(f"{name} 重试 40 次仍未下完（{have}/{total}）")
        request = urllib.request.Request(_sas_url(asset["id"]))
        if have:
            request.add_header("Range", f"bytes={have}-")
        try:
            with urllib.request.urlopen(request, timeout=45) as response, open(target, "ab") as file:
                while True:
                    chunk = response.read(256 * 1024)
                    if not chunk:
                        break
                    file.write(chunk)
        except Exception as exc:  # noqa: BLE001
            print(
                f"  第 {tries} 次中断（{type(exc).__name__}），已有 "
                f"{os.path.getsize(target) if os.path.exists(target) else 0}/{total} 字节，继续续传",
                flush=True,
            )
            time.sleep(1.5)
    print(f"{name} 完成 {os.path.getsize(target)} 字节", flush=True)
    return target


if __name__ == "__main__":
    if len(sys.argv) < 3:
        raise SystemExit(__doc__)
    out = sys.argv[3] if len(sys.argv) > 3 else "E:/6/apk82"
    download(sys.argv[1], sys.argv[2], out)
