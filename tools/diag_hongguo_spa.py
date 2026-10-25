"""从红果 SPA 里挖真实搜索接口。

红果 /search/<kw> 是 React SPA，结果靠前端 JS 现取。这里抓 /search 页面的
script src，下载 bundle，grep 出可能的 API 路径，并列出候选端点。

用法： python tools/diag_hongguo_spa.py [--proxy 127.0.0.1:20808]
"""
import argparse
import re
import socket
import ssl
import sys

HOST = "hongguoduanju.com"
MASK = "www.hongguoduanju.com"
UA = ("Mozilla/5.0 (Linux; Android 13) AppleWebKit/537.36 (KHTML, like Gecko) "
      "Chrome/120.0.0.0 Mobile Safari/537.36")


def fetch(path, sni=MASK, timeout=20, proxy=None, host=HOST):
    if proxy:
        h, p = proxy.split(":")
        raw = socket.create_connection((h, int(p)), timeout=timeout)
        raw.sendall((f"CONNECT {host}:443 HTTP/1.1\r\nHost: {host}:443\r\n\r\n").encode())
        head = b""
        while b"\r\n\r\n" not in head:
            c = raw.recv(4096)
            if not c:
                raise RuntimeError("代理 CONNECT 无响应")
            head += c
        if b" 200" not in head.split(b"\r\n")[0]:
            raise RuntimeError("代理 CONNECT 失败")
    else:
        raw = socket.create_connection((host, 443), timeout=timeout)
    ctx = ssl.create_default_context()
    ctx.check_hostname = False
    ctx.verify_mode = ssl.CERT_NONE
    sock = ctx.wrap_socket(raw, server_hostname=sni)
    req = (
        f"GET {path} HTTP/1.1\r\nHost: {host}\r\n"
        f"User-Agent: {UA}\r\nAccept: */*\r\n"
        f"Referer: https://{host}/\r\nConnection: close\r\n\r\n"
    )
    sock.sendall(req.encode())
    data = b""
    sock.settimeout(15)
    while True:
        try:
            c = sock.recv(65536)
        except (TimeoutError, socket.timeout):
            break
        if not c:
            break
        data += c
    sock.close()
    head, _, body = data.partition(b"\r\n\r\n")
    if b"transfer-encoding: chunked" in head.lower():
        out = b""
        while body:
            pos = body.find(b"\r\n")
            if pos < 0:
                break
            try:
                size = int(body[:pos].split(b";")[0], 16)
            except ValueError:
                out = out or body
                break
            if size == 0:
                break
            out += body[pos + 2:pos + 2 + size]
            body = body[pos + 2 + size + 2:]
        body = out
    return head.split(b"\r\n")[0].decode(errors="replace"), body


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--proxy", default=None)
    args = ap.parse_args()

    body = b""
    status = ""
    last = None
    for sni in (MASK, HOST):
        try:
            status, body = fetch("/search/%E9%83%BD%E5%B8%82", sni=sni, proxy=args.proxy)
        except Exception as exc:  # noqa: BLE001
            last = exc
            continue
        if body:
            print(f"页面 ({sni}):", status, len(body), "B")
            break
        print(f"页面 ({sni}) 空响应，换通道")
    if not body:
        print("页面抓取失败:", last)
        return

    text = body.decode("utf-8", errors="replace")
    scripts = re.findall(r'<script[^>]+src="([^"]+)"', text)
    print(f"script 数: {len(scripts)}")
    candidates = []
    for src in scripts:
        print("  ", src)
        if src.startswith("http"):
            rest = src.split("://", 1)[1]
            js_host, _, js_path = rest.partition("/")
            js_path = "/" + js_path
            sni = js_host
        else:
            js_host, js_path = HOST, ("" if src.startswith("/") else "/") + src
            sni = MASK
        try:
            st, js = fetch(js_path, sni=sni, host=js_host, proxy=args.proxy)
        except Exception as exc:  # noqa: BLE001
            print("      下载失败:", type(exc).__name__, exc)
            continue
        print("      ", st, len(js), "B")
        code = js.decode("utf-8", errors="replace")
        for m in set(re.findall(r'["\'](/[a-z0-9_\-/]*(?:search|suggest)[a-z0-9_\-/]*)["\']', code, re.I)):
            candidates.append((js_host + js_path, m))
        for m in set(re.findall(r'["\'](https?://[^"\']*(?:search|suggest)[^"\']*)["\']', code, re.I)):
            candidates.append((js_host + js_path, m))
    print("\n=== SPA 里出现的 search 相关路径 ===")
    for p, m in sorted(set(candidates)):
        print(f"  {m}      (来自 {p})")


if __name__ == "__main__":
    main()
