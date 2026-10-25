#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""直连复现七猫（qmplaylet）接口，判断是接口变了还是 App 逻辑问题。

用法：
    python tools/diag_qimao.py              # 拉推荐页并统计
    python tools/diag_qimao.py 都市          # 模拟 App 的本地标题匹配搜索
"""
import base64
import hashlib
import json
import ssl
import socket
import sys
import time
import urllib.parse

STORE = "https://api-store.qmplaylet.com"
READ = "https://api-read.qmplaylet.com"
KEYS = "d3dGiJc651gSQ8w1"
UA = "okhttp/3.12.11"

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
    raw = json.dumps(device, separators=(",", ":"), ensure_ascii=False).encode()
    encoded = base64.b64encode(raw).decode()
    return "".join(CHAR_MAP.get(ch, ch) for ch in encoded)


def param_sign(params):
    return hashlib.md5((params + KEYS).encode()).hexdigest()


def header_sign(params):
    text = ("AUTHORIZATION=app-version=10001application-id=com.duoduo.readchannel=unknown"
            "is-white=net-env=5platform=androidqm-params=" + params + "reg=" + KEYS)
    return hashlib.md5(text.encode()).hexdigest()


def http_get(url, timeout=15):
    parsed = urllib.parse.urlparse(url)
    host = parsed.hostname
    path = parsed.path + ("?" + parsed.query if parsed.query else "")
    raw = socket.create_connection((host, 443), timeout=timeout)
    ctx = ssl.create_default_context()
    tls = ctx.wrap_socket(raw, server_hostname=host)
    qp = qm_params()
    headers = [
        "GET %s HTTP/1.1" % path,
        "Host: %s" % host,
        "User-Agent: %s" % UA,
        "content-type: application/json; charset=utf-8",
        "net-env: 5",
        "reg: ",
        "channel: unknown",
        "is-white: ",
        "platform: android",
        "application-id: com.duoduo.read",
        "authorization: ",
        "app-version: 10001",
        "qm-params: %s" % qp,
        "sign: %s" % header_sign(qp),
        "Accept-Encoding: identity",
        "Connection: close",
    ]
    tls.sendall(("\r\n".join(headers) + "\r\n\r\n").encode())
    buf = b""
    tls.settimeout(timeout)
    while True:
        try:
            chunk = tls.recv(65536)
        except Exception:
            break
        if not chunk:
            break
        buf += chunk
    tls.close()
    head, _, body = buf.partition(b"\r\n\r\n")
    status = head.split(b"\r\n")[0].decode("latin-1", "replace")
    if b"chunked" in head.lower():
        out, rest = b"", body
        while True:
            pos = rest.find(b"\r\n")
            if pos < 0:
                break
            try:
                size = int(rest[:pos].split(b";")[0], 16)
            except ValueError:
                break
            if size == 0:
                break
            out += rest[pos + 2:pos + 2 + size]
            rest = rest[pos + 2 + size + 2:]
        body = out
    return status, body


def index_url(tag="0"):
    params = "operation=1playlet_privacy=1tag_id=" + tag
    return "%s/api/v1/playlet/index?tag_id=%s&playlet_privacy=1&operation=1&sign=%s" % (
        STORE, urllib.parse.quote(tag), param_sign(params))


def main():
    keyword = sys.argv[1] if len(sys.argv) > 1 else ""
    status, body = http_get(index_url("0"))
    print("推荐页(tag_id=0): %s  %d B" % (status, len(body)))
    try:
        data = json.loads(body.decode("utf-8"))
    except Exception as exc:
        print("  不是 JSON：%s" % exc)
        print("  前 300B: %r" % body[:300])
        return
    print("  顶层键: %s" % list(data.keys()))
    inner = data.get("data") if isinstance(data.get("data"), dict) else {}
    rows = inner.get("list") or []
    print("  code=%r  msg=%r  list=%d 条  tag_items=%d 个" % (
        data.get("code"), data.get("msg"), len(rows),
        len(inner.get("tag_items") or [])))
    titles = [(r.get("title"), r.get("playlet_id"), r.get("total_episode_num")) for r in rows
              if isinstance(r, dict)]
    print("  前 10 条:")
    for t in titles[:10]:
        print("    %s" % (t,))
    if keyword:
        needle = keyword.strip().lower()
        hits = [t for t in titles if t[0] and needle in str(t[0]).lower()]
        print("\n模拟 App 搜索（本地标题匹配 %r）：命中 %d / %d 条" % (keyword, len(hits), len(titles)))
        for h in hits[:10]:
            print("    %s" % (h,))
        tags = inner.get("tag_items") or []
        tag_hits = [(t.get("tag_id"), t.get("tag_name")) for t in tags
                    if isinstance(t, dict) and needle in str(t.get("tag_name", "")).lower()]
        print("  分类名命中: %s" % (tag_hits[:5],))
        print("  全部分类: %s" % ([(t.get("tag_id"), t.get("tag_name")) for t in tags if isinstance(t, dict)][:20],))


if __name__ == "__main__":
    main()
