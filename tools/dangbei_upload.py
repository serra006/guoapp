#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""通过当贝市场「远程推送」(/upload) 把本地文件推到电视盒子，免 U 盘。

用法：
    python tools/dangbei_upload.py 192.168.3.20 E:/6/apk79/xxx.apk
    python tools/dangbei_upload.py 192.168.3.20 E:/6/apk79/xxx.apk --port 5001
"""
import os
import socket
import sys
import uuid


def build_multipart(path, field="file1"):
    boundary = "----WebKitFormBoundary" + uuid.uuid4().hex
    name = os.path.basename(path).encode()
    with open(path, "rb") as fh:
        payload = fh.read()
    head = (
        "--%s\r\n"
        'Content-Disposition: form-data; name="%s"; filename="%s"\r\n'
        "Content-Type: application/vnd.android.package-archive\r\n\r\n" % (boundary, field, name.decode())
    ).encode()
    tail = ("\r\n--%s--\r\n" % boundary).encode()
    body = head + payload + tail
    return boundary, body


def upload(host, port, path, field="file1", timeout=600):
    boundary, body = build_multipart(path, field)
    req_head = (
        "POST /upload HTTP/1.1\r\n"
        "Host: %s:%d\r\n"
        "User-Agent: Mozilla/5.0\r\n"
        "Accept: */*\r\n"
        "Content-Type: multipart/form-data; boundary=%s\r\n"
        "Content-Length: %d\r\n"
        "Connection: close\r\n\r\n" % (host, port, boundary, len(body))
    ).encode()
    sock = socket.create_connection((host, port), timeout=20)
    sock.settimeout(timeout)
    sock.sendall(req_head + body)
    buf = b""
    while True:
        try:
            chunk = sock.recv(65536)
        except socket.timeout:
            break
        if not chunk:
            break
        buf += chunk
    sock.close()
    head, _, tail = buf.partition(b"\r\n\r\n")
    return head.decode("latin-1", "replace"), tail.decode("utf-8", "replace")


def main():
    if len(sys.argv) < 3:
        print(__doc__)
        return
    host, path = sys.argv[1], sys.argv[2]
    port = 5001
    if "--port" in sys.argv:
        port = int(sys.argv[sys.argv.index("--port") + 1])
    size = os.path.getsize(path)
    print("上传 %s (%d B) -> %s:%d/upload" % (os.path.basename(path), size, host, port))
    head, tail = upload(host, port, path)
    print("响应头:\n%s" % head[:600])
    print("响应体:\n%s" % tail[:600])


if __name__ == "__main__":
    main()
