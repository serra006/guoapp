#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""鬼片网 CDN 的掩码 SNI 可行性验证。

背景：鬼片各线路的视频挂在不同 CDN 域名上（v13.qqqrst.com / v2.ppqrrs.com /
v13.rstu6.com ...），本地 DNS 把这些子域劫持到 183.192.65.101（反诈页），
直连真 SNI 又被 RST。可能的绕法是「真实 IP + 用裸域做 SNI + Host 仍是子域」。

用法：
    python tools/diag_guipian_cdn.py v13.qqqrst.com /wjv1/.../index.m3u8
    python tools/diag_guipian_cdn.py v13.qqqrst.com /path/index.m3u8 --apex qqqrst.com
"""
import random
import socket
import ssl
import struct
import sys

UA = ("Mozilla/5.0 (Linux; Android 13; Pixel 7) AppleWebKit/537.36 "
      "(KHTML, like Gecko) Chrome/126.0.0.0 Mobile Safari/537.36")
FAKE = "183.192.65."  # 上海反诈劫持段的特征前缀


def dns_tcp(domain, server="223.5.5.5", timeout=6):
    """走 TCP 53 查 A 记录（UDP 53 在本机会被本地劫持成假地址）。"""
    tid = random.randint(0, 0xFFFF)
    query = struct.pack(">HHHHHH", tid, 0x0100, 1, 0, 0, 0)
    for part in domain.split("."):
        query += bytes([len(part)]) + part.encode()
    query += b"\x00" + struct.pack(">HH", 1, 1)
    conn = socket.create_connection((server, 53), timeout=timeout)
    try:
        conn.sendall(struct.pack(">H", len(query)) + query)
        size = struct.unpack(">H", recv_exact(conn, 2))[0]
        data = recv_exact(conn, size)
    finally:
        conn.close()

    def skip_name(buf, index):
        while index < len(buf):
            length = buf[index]
            if length & 0xC0 == 0xC0:
                return index + 2
            if length == 0:
                return index + 1
            index += length + 1
        return index

    index = 12 + (skip_name(data, 12) - 12) + 4  # 跳过 question(name + type + class)
    answers = struct.unpack(">H", data[6:8])[0]
    ips = []
    for _ in range(answers):
        index = skip_name(data, index)
        rtype, _rclass, _ttl, rdlen = struct.unpack(">HHIH", data[index:index + 10])
        index += 10
        if rtype == 1 and rdlen == 4:
            ips.append(socket.inet_ntoa(data[index:index + 4]))
        index += rdlen
    return ips


def recv_exact(conn, size):
    buf = b""
    while len(buf) < size:
        chunk = conn.recv(size - len(buf))
        if not chunk:
            break
        buf += chunk
    return buf


def fetch(ip, sni, host, path, timeout=10):
    raw = socket.create_connection((ip, 443), timeout=timeout)
    ctx = ssl.create_default_context()
    ctx.check_hostname = False
    ctx.verify_mode = ssl.CERT_NONE
    tls = ctx.wrap_socket(raw, server_hostname=sni)
    request = ("GET %s HTTP/1.1\r\nHost: %s\r\nUser-Agent: %s\r\n"
               "Referer: https://guipianwu.com/\r\nConnection: close\r\n\r\n" % (path, host, UA))
    tls.sendall(request.encode())
    tls.settimeout(timeout)
    data = b""
    while len(data) < 4096:
        try:
            chunk = tls.recv(4096)
        except Exception:
            break
        if not chunk:
            break
        data += chunk
    tls.close()
    head, _, body = data.partition(b"\r\n\r\n")
    return head.split(b"\r\n")[0].decode("latin-1", "replace"), body


def main():
    if len(sys.argv) < 3:
        print(__doc__)
        return
    vhost = sys.argv[1]
    path = sys.argv[2]
    apex = sys.argv[sys.argv.index("--apex") + 1] if "--apex" in sys.argv else vhost.split(".", 1)[1]
    print("== 目标 %s%s  (掩码域 %s)" % (vhost, path, apex))
    ips = dns_tcp(apex)
    real = [ip for ip in ips if not ip.startswith(FAKE)]
    print("   TCP-DNS %s -> %s" % (apex, ips or "（无 A 记录）"))
    print("   系统 DNS %s -> %s" % (vhost, safe_gethost(vhost)))
    if not real:
        print("   ✗ 没有拿到真实 IP，掩码方案不可行（DNS 侧已被完全接管）")
        return
    for ip in real[:3]:
        for sni in (apex, vhost):
            try:
                status, body = fetch(ip, sni, vhost, path)
                valid = body.lstrip().startswith(b"#EXTM3U")
                print("   %-16s SNI=%-22s %s  %s  %r" % (
                    ip, sni, status, "✓真 m3u8" if valid else "×非 m3u8", body[:60]))
            except Exception as exc:
                print("   %-16s SNI=%-22s ERR %s: %s" % (ip, sni, type(exc).__name__, str(exc)[:48]))


def safe_gethost(host):
    try:
        return socket.gethostbyname(host)
    except Exception as exc:
        return "ERR %s" % type(exc).__name__


if __name__ == "__main__":
    main()
