#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""枚举鬼片网一部剧的全部线路，提取每条线路的 m3u8 主机，逐个判定连通性。

目的：判断「换线路能否绕过 v13.rstu6.com 的 SNI 阻断」。
用法：python tools/diag_guipian_lines.py [剧集ID]
"""
import re
import socket
import ssl
import sys
import urllib.parse

sys.path.insert(0, "tools")
from diag_guipian import http_get, SITE, RE_NOW, RE_MEDIA_ANY, UA  # noqa: E402

RE_PLAY = re.compile(r"/play/(\d+)-(\d+)-(\d+)\.html")


def play_link_hosts(vid):
    """按线路序号聚合：line -> 第一条分集的播放页 URL"""
    status, body = http_get("%s/nv/%s.html" % (SITE, vid))
    links = RE_PLAY.findall(body)
    if not links:
        print("详情页没有 /play/ 链接，status=%s len=%d" % (status, len(body)))
        return {}
    per_line = {}
    for series, line, ep in links:
        per_line.setdefault(int(line), []).append(int(ep))
    print("详情页 %s  线路数=%d  %s" % (status[:12], len(per_line),
                                    {k: len(v) for k, v in sorted(per_line.items())}))
    return per_line


def media_of(vid, line):
    url = "%s/play/%s-%d-0.html" % (SITE, vid, line)
    try:
        status, body = http_get(url, referer="%s/nv/%s.html" % (SITE, vid))
    except Exception as exc:
        return None, "ERR %s" % exc
    now = RE_NOW.search(body)
    media = RE_MEDIA_ANY.search(body)
    target = (now.group(1) if now else (media.group(0) if media else None))
    return target, "%s %dB" % (status[:12], len(body))


def tcp_tls(host, path, sni=None, timeout=8):
    sni = sni or host
    try:
        ip = socket.gethostbyname(host)
    except Exception as exc:
        return "DNS-FAIL %s" % exc, None
    try:
        raw = socket.create_connection((ip, 443), timeout=timeout)
    except Exception as exc:
        return "TCP-FAIL %s ip=%s" % (type(exc).__name__, ip), ip
    ctx = ssl.create_default_context()
    ctx.check_hostname = False
    ctx.verify_mode = ssl.CERT_NONE
    try:
        tls = ctx.wrap_socket(raw, server_hostname=sni)
    except Exception as exc:
        raw.close()
        return "TLS-FAIL %s ip=%s sni=%s" % (type(exc).__name__, ip, sni), ip
    try:
        req = ("GET %s HTTP/1.1\r\nHost: %s\r\nUser-Agent: %s\r\n"
               "Referer: https://guipianwu.com/\r\nConnection: close\r\n\r\n"
               % (path, host, UA))
        tls.sendall(req.encode())
        tls.settimeout(timeout)
        data = b""
        while len(data) < 600:
            try:
                chunk = tls.recv(600)
            except Exception:
                break
            if not chunk:
                break
            data += chunk
        first = data.split(b"\r\n")[0].decode("latin-1", "replace") if data else "(no data)"
        return "OK %s ip=%s sni=%s" % (first, ip, sni), ip
    except Exception as exc:
        return "READ-FAIL %s ip=%s" % (type(exc).__name__, ip), ip
    finally:
        tls.close()


def main():
    vid = sys.argv[1] if len(sys.argv) > 1 else "63217"
    per_line = play_link_hosts(vid)
    if not per_line:
        return
    results = []
    for line in sorted(per_line):
        target, info = media_of(vid, line)
        if not target:
            print("线路 %d: 播放页无地址 (%s)" % (line, info))
            continue
        p = urllib.parse.urlparse(target)
        print("\n线路 %d: %s" % (line, target))
        print("  播放页 %s" % info)
        status, _ = tcp_tls(p.hostname, p.path + ("?" + p.query if p.query else ""))
        print("  直连(SNI=%s): %s" % (p.hostname, status))
        results.append((line, p.hostname, status))
    print("\n=== 汇总 ===")
    for line, host, status in results:
        print("  线路%-3d %-28s %s" % (line, host, status[:70]))


if __name__ == "__main__":
    main()
