#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""探测电视盒子的 HTTP 管理页，判断是否有 adb 开关之类入口。

用法：python tools/probe_box.py 192.168.3.20 5001 5566
"""
import re
import socket
import sys


def fetch(host, port, path="/", timeout=6):
    s = socket.socket()
    s.settimeout(timeout)
    s.connect((host, port))
    req = "GET %s HTTP/1.0\r\nHost: %s\r\nUser-Agent: Mozilla/5.0\r\n\r\n" % (path, host)
    s.sendall(req.encode())
    buf = b""
    while True:
        try:
            chunk = s.recv(65536)
        except Exception:
            break
        if not chunk:
            break
        buf += chunk
    s.close()
    return buf


def main():
    host = sys.argv[1] if len(sys.argv) > 1 else "192.168.3.20"
    ports = [int(x) for x in sys.argv[2:]] or [5001, 5566]
    for port in ports:
        try:
            raw = fetch(host, port)
        except Exception as exc:
            print("=== %s:%d  连接失败 %s" % (host, port, exc))
            continue
        head, _, body = raw.partition(b"\r\n\r\n")
        text = body.decode("utf-8", "replace")
        title = re.findall(r"<title>(.*?)</title>", text, re.S)
        print("=== %s:%d  %d B  title=%r" % (host, port, len(text), title[:1]))
        print("    首行: %s" % head.split(b"\r\n")[0].decode("latin-1", "replace"))
        plain = re.sub(r"<script.*?</script>", "", text, flags=re.S)
        plain = re.sub(r"<style.*?</style>", "", plain, flags=re.S)
        plain = re.sub(r"<[^>]+>", " ", plain)
        plain = re.sub(r"\s+", " ", plain).strip()
        print("    正文: %s" % plain[:500])
        for kw in ["adb", "ADB", "调试", "debug", "root", "shell", "install", "投屏", "设置"]:
            count = text.count(kw)
            if count:
                print("    关键字 %-8s %d 次" % (kw, count))
        # 把页面里的 a href / form action / 常见接口都列出来
        links = sorted(set(re.findall(r'(?:href|action|src)="([^"]{1,80})"', text)))
        if links:
            print("    链接: %s" % links[:20])
        print()


if __name__ == "__main__":
    main()
