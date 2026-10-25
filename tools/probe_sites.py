#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""快速探测站源域名：DNS(UDP/TCP) + TCP443 + TLS 握手 + HTTP 状态。

用法：python tools/probe_sites.py guipianwu.com api-store.qmplaylet.com
"""
import socket
import ssl
import sys
import time


def dns_both(host):
    out = {}
    for proto, fn in (("udp", socket.getaddrinfo),):
        try:
            infos = fn(host, 443, socket.AF_INET, socket.SOCK_STREAM)
            out["A"] = sorted({i[4][0] for i in infos})
        except Exception as exc:
            out["A"] = "ERR %s" % exc
    # 用公共 DNS 走 TCP 复核（本地 UDP 常被劫持）
    try:
        import struct

        def tcp_query(server, name):
            # 极简 DNS 查询器（A 记录）
            tid = b"\x12\x34"
            q = tid + b"\x01\x00\x00\x01\x00\x00\x00\x00\x00\x00"
            for part in name.split("."):
                q += bytes([len(part)]) + part.encode()
            q += b"\x00\x00\x01\x00\x01"
            s = socket.socket()
            s.settimeout(4)
            s.connect((server, 53))
            s.sendall(len(q).to_bytes(2, "big") + q)
            length = int.from_bytes(s.recv(2), "big")
            data = b""
            while len(data) < length:
                chunk = s.recv(length - len(data))
                if not chunk:
                    break
                data += chunk
            s.close()
            # 粗略提取末尾 4 字节形式的 A 记录
            ips = []
            i = 12
            # 跳过问题段
            while i < len(data) and data[i] != 0:
                i += data[i] + 1
            i += 5
            while i + 12 <= len(data):
                if data[i] & 0xC0 == 0xC0:
                    i += 2
                else:
                    while i < len(data) and data[i] != 0:
                        i += data[i] + 1
                    i += 1
                if i + 10 > len(data):
                    break
                rtype = int.from_bytes(data[i:i + 2], "big")
                rdlen = int.from_bytes(data[i + 8:i + 10], "big")
                i += 10
                if rtype == 1 and rdlen == 4:
                    ips.append(".".join(str(b) for b in data[i:i + 4]))
                i += rdlen
            return ips

        out["TCP53@223.5.5.5"] = tcp_query("223.5.5.5", host)
    except Exception as exc:
        out["TCP53@223.5.5.5"] = "ERR %s" % exc
    return out


def tls_http(host, path="/", sni=None, host_header=None):
    sni = sni or host
    host_header = host_header or host
    result = {}
    try:
        ip = socket.gethostbyname(host)
        result["ip"] = ip
        raw = socket.create_connection((ip, 443), timeout=8)
        result["tcp"] = "ok"
    except Exception as exc:
        result["tcp"] = "ERR %s" % exc
        return result
    ctx = ssl.create_default_context()
    ctx.check_hostname = False
    ctx.verify_mode = ssl.CERT_NONE
    try:
        t0 = time.time()
        tls = ctx.wrap_socket(raw, server_hostname=sni)
        result["tls"] = "ok %.2fs %s" % (time.time() - t0, tls.version())
    except Exception as exc:
        result["tls"] = "ERR %s: %s" % (type(exc).__name__, exc)
        raw.close()
        return result
    try:
        req = "GET %s HTTP/1.1\r\nHost: %s\r\nUser-Agent: Mozilla/5.0 (Linux; Android 9) AppleWebKit/537.36 Chrome/120 Mobile Safari/537.36\r\nAccept: text/html\r\nConnection: close\r\n\r\n" % (
            path, host_header)
        tls.sendall(req.encode())
        buf = b""
        tls.settimeout(10)
        while len(buf) < 4096:
            try:
                chunk = tls.recv(4096)
            except Exception:
                break
            if not chunk:
                break
            buf += chunk
        first = buf.split(b"\r\n")[0].decode("latin-1", "replace")
        result["http"] = "%s (%d B)" % (first, len(buf))
    except Exception as exc:
        result["http"] = "ERR %s: %s" % (type(exc).__name__, exc)
    finally:
        tls.close()
    return result


def main():
    hosts = sys.argv[1:]
    for host in hosts:
        print("=" * 62)
        print(host)
        for k, v in dns_both(host).items():
            print("  DNS %-16s %s" % (k, v))
        for k, v in tls_http(host).items():
            print("  %-4s %-16s %s" % ("", k, v))


if __name__ == "__main__":
    main()
