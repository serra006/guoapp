"""诊断红果搜索页：SNI 掩码 vs 裸 SNI，看 /search/<kw> 到底返回什么。

用法：
  python tools/diag_hongguo_search.py [关键词] [--proxy 127.0.0.1:20808]

关注点：
  1. 状态码 / Content-Length；
  2. 响应体里有没有 searchList / isSuccess / query 字段（Go 侧解析靠这些）；
  3. 是否被降级成错误页（例如“暂时无法加载”）。
"""
import argparse
import re
import socket
import ssl
import sys
import urllib.parse

HOST = "hongguoduanju.com"
MASK = "www.hongguoduanju.com"
UA = ("Mozilla/5.0 (iPhone; CPU iPhone OS 18_7 like Mac OS X) AppleWebKit/605.1.15 "
      "(KHTML, like Gecko) Version/26.6 Mobile/15E148 Safari/604.1")


def fetch(path, sni, timeout=20, proxy=None):
    if proxy:
        host, port = proxy.split(":")
        raw = socket.create_connection((host, int(port)), timeout=timeout)
        raw.sendall((f"CONNECT {HOST}:443 HTTP/1.1\r\nHost: {HOST}:443\r\n\r\n").encode())
        head = b""
        while b"\r\n\r\n" not in head:
            chunk = raw.recv(4096)
            if not chunk:
                raise RuntimeError("代理 CONNECT 无响应")
            head += chunk
        if b" 200" not in head.split(b"\r\n")[0]:
            raise RuntimeError("代理 CONNECT 失败: " + head.split(b"\r\n")[0].decode(errors="replace"))
    else:
        raw = socket.create_connection((HOST, 443), timeout=timeout)

    ctx = ssl.create_default_context()
    # 诊断用：App 里 Go 的证书校验是按 URL 的 Host（hongguoduanju.com）做的，
    # 不是按握手 SNI。Python 默认按 SNI 校验，掩码通道必然报 hostname mismatch，
    # 所以这里关掉校验，只看服务端实际回了什么。
    ctx.check_hostname = False
    ctx.verify_mode = ssl.CERT_NONE
    try:
        sock = ctx.wrap_socket(raw, server_hostname=sni)
        cert = sock.getpeercert()
        print(f"  证书 subject={cert.get('subject')}  SAN={cert.get('subjectAltName')}")
    except Exception:
        raw.close()
        raise

    req = (
        f"GET {path} HTTP/1.1\r\n"
        f"Host: {HOST}\r\n"
        f"User-Agent: {UA}\r\n"
        "Accept: text/html,application/xhtml+xml\r\n"
        "Accept-Language: zh-CN,zh;q=0.9\r\n"
        f"Referer: https://{HOST}/\r\n"
        "Connection: close\r\n\r\n"
    )
    sock.sendall(req.encode())
    data = b""
    sock.settimeout(10)
    while True:
        try:
            chunk = sock.recv(65536)
        except (TimeoutError, socket.timeout):
            # 服务端不一定按 Connection: close 断开，读到超时就当收完。
            break
        if not chunk:
            break
        data += chunk
        if b"\r\n0\r\n\r\n" in data[-8:] or data.endswith(b"\r\n0\r\n\r\n"):
            break
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


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("keyword", nargs="?", default="都市")
    ap.add_argument("--proxy", default=None)
    args = ap.parse_args()

    path = "/search/" + urllib.parse.quote(args.keyword)
    print(f"GET {path}   proxy={args.proxy or '直连'}")

    for label, sni in (("掩码SNI(www)", MASK), ("裸SNI", HOST)):
        try:
            raw = fetch(path, sni, proxy=args.proxy)
        except Exception as exc:  # noqa: BLE001
            print(f"  [{label}] 失败: {type(exc).__name__}: {exc}")
            continue
        head, _, body = raw.partition(b"\r\n\r\n")
        status = head.split(b"\r\n")[0].decode(errors="replace")
        chunked = b"transfer-encoding: chunked" in head.lower()
        if chunked:
            body = dechunk(body)
        text = body.decode("utf-8", errors="replace")
        print(f"  [{label}] {status}  body={len(body)}B  chunked={chunked}")
        for key in ("searchList", "isSuccess", "totalCount", "\"query\"", "暂时无法加载"):
            hit = key in text
            print(f"      {key:16} {'有' if hit else '无'}")
        m = re.search(r"searchList", text)
        if m:
            print("      片段:", re.sub(r"\s+", " ", text[max(0, m.start() - 80):m.start() + 240])[:320])
        else:
            print("      正文开头:", re.sub(r"\s+", " ", text[:200]))


if __name__ == "__main__":
    main()
