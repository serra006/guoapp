"""诊断红果「搜索联想」接口 —— 这是红果搜索唯一的真实数据来源。

红果 2026-10 起 /search/<kw> 改成 React SPA，HTML 里没有 searchList；
App 的搜索完全依赖 /incent_resource/suggestion?app_id=8662&query=<kw>&count=50
（一级联想）以及用一级联想里的剧名词再查一次（二级联想）。

用法：
  python tools/diag_hongguo_suggest.py [关键词...] [--proxy 127.0.0.1:20808] [--count 50]
"""
import argparse
import json
import socket
import ssl
import sys
import urllib.parse

HOST = "hongguoduanju.com"
MASK = "www.hongguoduanju.com"
UA = ("Mozilla/5.0 (Linux; Android 13; apollo_p1) AppleWebKit/537.36 "
      "(KHTML, like Gecko) Chrome/120.0.0.0 Mobile Safari/537.36")


def fetch(path, sni, timeout=20, proxy=None):
    if proxy:
        h, p = proxy.split(":")
        raw = socket.create_connection((h, int(p)), timeout=timeout)
        raw.sendall((f"CONNECT {HOST}:443 HTTP/1.1\r\nHost: {HOST}:443\r\n\r\n").encode())
        head = b""
        while b"\r\n\r\n" not in head:
            c = raw.recv(4096)
            if not c:
                raise RuntimeError("代理 CONNECT 无响应")
            head += c
        if b" 200" not in head.split(b"\r\n")[0]:
            raise RuntimeError("代理 CONNECT 失败: " + head.split(b"\r\n")[0].decode(errors="replace"))
    else:
        raw = socket.create_connection((HOST, 443), timeout=timeout)

    ctx = ssl.create_default_context()
    ctx.check_hostname = False
    ctx.verify_mode = ssl.CERT_NONE
    sock = ctx.wrap_socket(raw, server_hostname=sni)

    req = (
        f"GET {path} HTTP/1.1\r\n"
        f"Host: {HOST}\r\n"
        "Accept: application/json\r\n"
        "Accept-Language: zh-CN,zh;q=0.9\r\n"
        f"Referer: https://{HOST}/\r\n"
        f"User-Agent: {UA}\r\n"
        "Connection: close\r\n\r\n"
    )
    sock.sendall(req.encode())
    data = b""
    sock.settimeout(10)
    while True:
        try:
            c = sock.recv(65536)
        except (TimeoutError, socket.timeout):
            break
        if not c:
            break
        data += c
    sock.close()
    return data


def dechunk(body):
    out = b""
    while body:
        pos = body.find(b"\r\n")
        if pos < 0:
            break
        try:
            size = int(body[:pos].split(b";")[0], 16)
        except ValueError:
            return out or body
        if size == 0:
            break
        out += body[pos + 2:pos + 2 + size]
        body = body[pos + 2 + size + 2:]
    return out


def probe(query, count, proxy):
    params = urllib.parse.urlencode({"app_id": "8662", "query": query, "count": str(count)})
    path = "/incent_resource/suggestion?" + params
    print(f"\n=== query={query!r} count={count} ===")
    for label, sni in (("掩码SNI(www)", MASK), ("裸SNI", HOST)):
        try:
            raw = fetch(path, sni, proxy=proxy)
        except Exception as exc:  # noqa: BLE001
            print(f"  [{label}] 失败: {type(exc).__name__}: {exc}")
            continue
        head, _, body = raw.partition(b"\r\n\r\n")
        status = head.split(b"\r\n")[0].decode(errors="replace")
        if b"transfer-encoding: chunked" in head.lower():
            body = dechunk(body)
        text = body.decode("utf-8", errors="replace")
        print(f"  [{label}] {status}  body={len(body)}B")
        if body[:1] not in (b"{", b"["):
            print("    非 JSON，开头:", text[:160].replace("\n", " "))
            continue
        try:
            data = json.loads(text)
        except Exception as exc:  # noqa: BLE001
            print("    JSON 解析失败:", exc, text[:160])
            continue
        items = data.get("suggest_list") or (data.get("data") or {}).get("suggest_list") or []
        print(f"    suggest_list={len(items)} 条")
        with_series = 0
        for it in items[:12]:
            vd = it.get("video_data") or {}
            sid = vd.get("series_id_str") or vd.get("series_id")
            name = it.get("name", "")
            wt = it.get("word_type", "")
            if sid:
                with_series += 1
            print(f"      [{wt:20}] series_id={str(sid):<12} name={name}")
        total_series = sum(
            1 for it in items
            if (it.get("video_data") or {}).get("series_id_str") or (it.get("video_data") or {}).get("series_id")
        )
        print(f"    带 series_id 的共 {total_series} 条（可转成剧集）")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("keywords", nargs="*", default=["都市"])
    ap.add_argument("--proxy", default=None)
    ap.add_argument("--count", type=int, default=50)
    args = ap.parse_args()
    for kw in args.keywords:
        probe(kw, args.count, args.proxy)


if __name__ == "__main__":
    main()
