#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""复现鬼片网（guipianwu.com）的解析链路，定位「无法播放」卡在哪一步。

对照 Go 侧 provider_guipian.go 的正则，逐步报告命中情况：
    列表卡片 -> 详情页线路 tab -> 分集链接 -> 播放页 m3u8

用法：python tools/diag_guipian.py
      python tools/diag_guipian.py --id 12345
"""
import re
import socket
import ssl
import sys
import urllib.parse

SITE = "https://guipianwu.com"
UA = ("Mozilla/5.0 (Linux; Android 13; Pixel 7) AppleWebKit/537.36 "
      "(KHTML, like Gecko) Chrome/126.0.0.0 Mobile Safari/537.36")

FLAGS = re.S | re.I
RE_MAIN_LIST = re.compile(r'<ul class="row video-list video-film-list[^"]*">(.*?)</ul>', FLAGS)
RE_CARD = re.compile(r'<a class="video-link" href="/nv/(\d+)\.html"[^>]*title="([^"]*)"(.*?)</a>', FLAGS)
RE_TAB = re.compile(r'<li class="swiper-slide ewave-tab[^"]*" data-target="#(ewave-playlist-\d+)">([^<]+)</li>', FLAGS)
RE_EPISODE = re.compile(r"<li><a title='([^']*)' href='([^']+)'", FLAGS)
RE_NOW = re.compile(r'var\s+now\s*=\s*["\']([^"\']+)["\']', FLAGS)
RE_MEDIA_ANY = re.compile(r'https?://[^"\'\s<>\\]+\.(?:m3u8|mp4)(?:\?[^"\'\s<>\\]*)?', re.I)
RE_PLAYLIST_URL = re.compile(r'(/play/[^\s"\'<>]+\.html)', re.I)


def http_get(url, referer=None, timeout=15):
    parsed = urllib.parse.urlparse(url)
    host = parsed.hostname
    path = parsed.path + ("?" + parsed.query if parsed.query else "")
    raw = socket.create_connection((host, 443), timeout=timeout)
    ctx = ssl.create_default_context()
    ctx.check_hostname = False
    ctx.verify_mode = ssl.CERT_NONE
    tls = ctx.wrap_socket(raw, server_hostname=host)
    headers = [
        "GET %s HTTP/1.1" % path,
        "Host: %s" % host,
        "User-Agent: %s" % UA,
        "Accept: text/html,application/xhtml+xml,*/*;q=0.8",
        "Accept-Language: zh-CN,zh;q=0.9",
        "Accept-Encoding: identity",
    ]
    if referer:
        headers.append("Referer: %s" % referer)
    headers += ["Connection: close", "", ""]
    tls.sendall("\r\n".join(headers).encode())
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
    return status, body.decode("utf-8", "replace")


def step_list():
    for path in ("/", "/list/3.html", "/list/3_2.html"):
        try:
            status, body = http_get(SITE + path)
        except Exception as exc:
            print("  %-16s ERR %s" % (path, exc))
            continue
        main = RE_MAIN_LIST.search(body)
        cards = RE_CARD.findall(main.group(1) if main else body)
        print("  %-16s %s  %dB  主容器=%s  卡片=%d" % (
            path, status[:12], len(body), "有" if main else "无", len(cards)))
        if cards:
            print("      前 3 条: %s" % [(c[0], c[1]) for c in cards[:3]])
            return cards[0][0]
    return None


def step_detail(vid):
    url = "%s/nv/%s.html" % (SITE, vid)
    status, body = http_get(url)
    print("  详情 %s  %s  %dB" % (url, status[:12], len(body)))
    tabs = RE_TAB.findall(body)
    print("    线路 tab 命中: %d  %s" % (len(tabs), tabs[:4]))
    eps = RE_EPISODE.findall(body)
    print("    分集链接命中: %d" % len(eps))
    if eps:
        print("      前 3 条: %s" % (eps[:3],))
    # 站点真实链接形态
    samples = RE_PLAYLIST_URL.findall(body)[:5]
    print("    页面里 /play/*.html 形态样例: %s" % (samples,))
    if not eps:
        # 兜底：把页面里所有 a href 里像分集的列出来
        hrefs = re.findall(r"href='([^']+)'", body)[:20] or re.findall(r'href="([^"]+)"', body)[:20]
        print("    页面 href 样例: %s" % (hrefs[:12],))
    return eps, body


def step_play(href):
    if href.startswith("http"):
        url = href
    elif href.startswith("/"):
        url = SITE + href
    else:
        url = SITE + "/" + href
    status, body = http_get(url, referer=SITE + "/")
    print("  播放页 %s  %s  %dB" % (url, status[:12], len(body)))
    now = RE_NOW.search(body)
    media = RE_MEDIA_ANY.search(body)
    print("    var now 命中: %s" % (now.group(1) if now else "无"))
    print("    页面内 m3u8/mp4: %s" % (media.group(0) if media else "无"))
    if not now and not media:
        # 看看是不是播放器换成了 JS 里拼地址
        for kw in ("m3u8", ".mp4", "url:", "source", "player"):
            idx = body.find(kw)
            if idx >= 0:
                snippet = re.sub(r"\s+", " ", body[max(0, idx - 120):idx + 180])
                print("    [%s] %s" % (kw, snippet[:240]))
                break
    return url


def main():
    vid = None
    if "--id" in sys.argv:
        vid = sys.argv[sys.argv.index("--id") + 1]
    print("=== 1) 列表页")
    if not vid:
        vid = step_list()
    if not vid:
        print("列表页没拿到卡片，后续步骤无法进行")
        return
    print("\n=== 2) 详情页 (id=%s)" % vid)
    eps, _ = step_detail(vid)
    if not eps:
        print("详情页没有分集链接 -> App 会报「线路暂无可播放分集」")
        return
    print("\n=== 3) 播放页")
    href = eps[0][1]
    print("  原始 href: %r" % href)
    # Go 侧 guipianPlayURL 的校验：必须 http(s)、同源、路径含 /play/、以 .html 结尾
    abs_url = href if href.startswith("http") else SITE + "/" + href.lstrip("/")
    p = urllib.parse.urlparse(abs_url)
    ok_host = p.hostname in ("guipianwu.com", "www.guipianwu.com")
    ok_path = p.path.lower().endswith(".html") and "/play/" in p.path
    print("  Go 校验: 同源=%s  /play/+*.html=%s  -> %s" % (
        ok_host, ok_path, "通过" if ok_host and ok_path else "会被拒（播放地址无效）"))
    if ok_host and ok_path:
        step_play(abs_url)


if __name__ == "__main__":
    main()
