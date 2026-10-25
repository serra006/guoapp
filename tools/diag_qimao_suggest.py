"""探测七猫的「搜索提示词 / 联想」接口。

背景：电视端只有遥控器，中文剧名必须靠联想下拉转出来（红果有，七猫没有）。
这里按 provider_qimao.go 的签名规则复刻请求，逐个试候选提示词端点。

用法：
  python tools/diag_qimao_suggest.py [关键词]
  python tools/diag_qimao_suggest.py dushi --proxy 127.0.0.1:20808
"""
import argparse
import base64
import hashlib
import json
import socket
import ssl
import time
import urllib.parse

KEYS = "d3dGiJc651gSQ8w1"
UA = "okhttp/3.12.11"
STORE = "api-store.qmplaylet.com"
READ = "api-read.qmplaylet.com"

CHAR_MAP = {
    '+': 'P', '/': 'X', '0': 'M', '1': 'U', '2': 'l', '3': 'E', '4': 'r', '5': 'Y', '6': 'W', '7': 'b', '8': 'd', '9': 'J',
    'A': '9', 'B': 's', 'C': 'a', 'D': 'I', 'E': '0', 'F': 'o', 'G': 'y', 'H': '_', 'I': 'H', 'J': 'G', 'K': 'i', 'L': 't',
    'M': 'g', 'N': 'N', 'O': 'A', 'P': '8', 'Q': 'F', 'R': 'k', 'S': '3', 'T': 'h', 'U': 'f', 'V': 'R', 'W': 'q', 'X': 'C',
    'Y': '4', 'Z': 'p', 'a': 'm', 'b': 'B', 'c': 'O', 'd': 'u', 'e': 'c', 'f': '6', 'g': 'K', 'h': 'x', 'i': '5', 'j': 'T',
    'k': '-', 'l': '2', 'm': 'z', 'n': 'S', 'o': 'Z', 'p': '1', 'q': 'V', 'r': 'v', 's': 'j', 't': 'Q', 'u': '7', 'v': 'D',
    'w': 'w', 'x': 'n', 'y': 'L', 'z': 'e',
}


def qm_params():
    device = {
        "static_score": "0.8",
        "uuid": "00000000-7fc7-08dc-0000-000000000000",
        "device-id": "20250220125449b9b8cac84c2dd3d035c9052a2572f7dd0122edde3cc42a70",
        "sourceuid": "aa7de295aad621a6",
        "refresh-type": "0",
        "model": "22021211RC",
        "client-id": "aa7de295aad621a6",
        "brand": "Redmi",
        "sys-ver": "12",
        "phone-level": "H",
        "wlb-uid": "aa7de295aad621a6",
        "session-id": str(int(time.time() * 1000)),
    }
    raw = json.dumps(device, separators=(",", ":")).encode()
    return "".join(CHAR_MAP.get(c, c) for c in base64.b64encode(raw).decode())


def param_sign(params):
    return hashlib.md5((params + KEYS).encode()).hexdigest()


def header_sign(params):
    prefix = ("AUTHORIZATION=app-version=10001application-id=com.duoduo.read"
              "channel=unknownis-white=net-env=5platform=androidqm-params=")
    return hashlib.md5((prefix + params + "reg=" + KEYS).encode()).hexdigest()


def fetch(host, path, proxy=None, timeout=20):
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
    sock = ctx.wrap_socket(raw, server_hostname=host)
    params = qm_params()
    headers = [
        ("User-Agent", UA),
        ("content-type", "application/json; charset=utf-8"),
        ("net-env", "5"), ("reg", ""), ("channel", "unknown"), ("is-white", ""),
        ("platform", "android"), ("application-id", "com.duoduo.read"),
        ("authorization", ""), ("app-version", "10001"),
        ("qm-params", params), ("sign", header_sign(params)),
    ]
    req = f"GET {path} HTTP/1.1\r\nHost: {host}\r\n"
    req += "".join(f"{k}: {v}\r\n" for k, v in headers)
    req += "Accept: application/json\r\nConnection: close\r\n\r\n"
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


def build(host_path, pairs):
    """pairs: [(参数名, 值)]；签名按参数名字典序拼接，值用未转义原文。"""
    params = "".join(f"{k}={v}" for k, v in sorted(pairs))
    query = "&".join(f"{k}={urllib.parse.quote(str(v))}" for k, v in pairs)
    return f"{host_path}?{query}&sign={param_sign(params)}", params


CANDIDATES = [
    (STORE, "/api/v1/playlet/suggest", [("wd", "{q}")]),
    (STORE, "/api/v1/playlet/search/suggest", [("wd", "{q}")]),
    (STORE, "/api/v1/search/suggest", [("wd", "{q}")]),
    (STORE, "/api/v1/playlet/search/tips", [("wd", "{q}")]),
    (STORE, "/api/v1/playlet/hots", []),
    (STORE, "/api/v1/playlet/hot", []),
    (STORE, "/api/v1/playlet/recommend_words", []),
    (READ, "/player/api/v1/playlet/suggest", [("wd", "{q}")]),
    (READ, "/player/api/v1/suggest", [("wd", "{q}")]),
]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("keyword", nargs="?", default="dushi")
    ap.add_argument("--proxy", default=None)
    args = ap.parse_args()
    kw = args.keyword

    # 基线：现有搜索接口，看拼音能不能直接命中
    pairs = [("page", 1), ("wd", kw)]
    path, _ = build("/api/v1/playlet/search", pairs)
    st, body = fetch(STORE, path, proxy=args.proxy)
    text = body.decode("utf-8", errors="replace")
    print(f"[基线 search wd={kw}] {st} {len(body)}B")
    try:
        data = json.loads(text)
        rows = ((data.get("data") or {}).get("list")) or []
        print(f"    list={len(rows)} 条")
        for row in rows[:8]:
            print("      ", row.get("title"), "| id=", row.get("id"))
    except Exception:
        print("   ", text[:200])

    print("\n=== 候选提示词接口 ===")
    for host, p, tmpl in CANDIDATES:
        pairs = [(k, v.format(q=kw)) for k, v in tmpl]
        path, _ = build(p, pairs)
        try:
            st, body = fetch(host, path, proxy=args.proxy)
        except Exception as exc:  # noqa: BLE001
            print(f"  {host}{p:45} 失败 {type(exc).__name__}: {exc}")
            continue
        text = body.decode("utf-8", errors="replace")
        flag = "OK " if st.endswith("200 OK") else "   "
        snippet = json.dumps(json.loads(text), ensure_ascii=False)[:180] if text[:1] in "{[" else text[:120].replace("\n", " ")
        print(f"  {flag}{host}{p:45} {len(body):>7}B  {snippet}")


if __name__ == "__main__":
    main()
