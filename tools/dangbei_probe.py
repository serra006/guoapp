#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""抓取当贝远程推送页（5001）与文件快传页（5566），定位上传/截图接口。

用法：python tools/dangbei_probe.py 192.168.3.20
"""
import re
import sys
import os

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from probe_box import fetch  # noqa: E402


def main():
    host = sys.argv[1] if len(sys.argv) > 1 else "192.168.3.20"
    out_dir = r"E:\6\box"
    os.makedirs(out_dir, exist_ok=True)
    for port in (5001, 5566):
        raw = fetch(host, port, "/")
        head, _, body = raw.partition(b"\r\n\r\n")
        path = os.path.join(out_dir, "index_%d.html" % port)
        with open(path, "wb") as fh:
            fh.write(body)
        text = body.decode("utf-8", "replace")
        print("=== %s:%d -> %s" % (host, port, path))
        # 找 snapshot / upload 相关片段
        for kw in ("snapshot", "upload", "/cmd", "setInterval", "ajax", "$.get", "$.post"):
            for m in re.finditer(re.escape(kw), text):
                snippet = text[max(0, m.start() - 120):m.start() + 200]
                snippet = re.sub(r"\s+", " ", snippet)
                print("  [%s] %s" % (kw, snippet[:260]))
                break
        print()


if __name__ == "__main__":
    main()
