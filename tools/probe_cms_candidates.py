#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""苹果CMS 候选源实测：按线路（$$$ 分隔）逐条取首集，验证能否真解码。

判据：必须拿到 m3u8 -> 取首个分片 -> 验同步字节 0x47 / CT=video/mp2t
      才算「可用」；只看到 m3u8 地址不算。
"""
import json
import os
import ssl
import sys
import urllib.parse
import urllib.request

CTX = ssl.create_default_context()
CTX.check_hostname = False
CTX.verify_mode = ssl.CERT_NONE

UA = ("Mozilla/5.0 (Linux; Android 12; PGT-AN10) AppleWebKit/537.36 "
      "(KHTML, like Gecko) Chrome/120.0.0.0 Mobile Safari/537.36")

PROXY = os.environ.get("PROBE_PROXY") or ""
if PROXY:
    OPENER = urllib.request.build_opener(
        urllib.request.ProxyHandler({"http": PROXY, "https": PROXY}),
        urllib.request.HTTPSHandler(context=CTX))
else:
    OPENER = urllib.request.build_opener(
        urllib.request.HTTPSHandler(context=CTX))


def q(u):
    return urllib.parse.quote(u, safe=":/?#[]@!$&'()*+,;=%")


def get(u, headers=None, timeout=15, limit=None):
    h = {"User-Agent": UA}
    if headers:
        h.update(headers)
    req = urllib.request.Request(q(u), headers=h)
    with OPENER.open(req, timeout=timeout) as r:
        data = r.read() if limit is None else r.read(limit)
        return r.status, dict(r.headers), data


SITES = [
    # name, api base, t (短剧分类), ids 是否用于采样
    ("魔都", "https://www.mdzyapi.com/api.php/provide/vod/", "38"),
    ("360", "https://360zy.com/api.php/provide/vod/", "46"),
    ("艾旦", "https://lovedan.net/api.php/provide/vod/", ""),
    ("麒麟", "https://www.qlys.cc/api.php/provide/vod/", ""),
    ("巨量", "https://api.juliang.live/api.php/provide/vod/", ""),
    ("非凡", "https://ffzy5.tv/api.php/provide/vod/", ""),
]


def first_episode(vod_play_url):
    """返回 [(线路序号, 集名, url), ...] —— 每条线路取第一集"""
    out = []
    if not vod_play_url:
        return out
    for li, line in enumerate(vod_play_url.split("$$$")):
        eps = [e for e in line.split("#") if e.strip()]
        for ep in eps[:1]:
            if "$" not in ep:
                continue
            name, _, url = ep.partition("$")
            out.append((li, name, url.strip()))
    return out


def _m3u8_first_ref(url, depth=0, chain=""):
    """递归下钻：主列表 -> 子列表 -> TS 分片。返回 (ok, desc)"""
    if depth > 4:
        return False, "嵌套过深 %s" % chain
    try:
        st, hdr, body = get(url, timeout=15, limit=16384)
    except Exception as e:
        return False, "m3u8 取不到: %s" % e
    txt = body.decode("utf-8", "ignore")
    if "#EXTM3U" not in txt:
        head = txt.strip().replace("\n", " ")[:70]
        return False, "不是 m3u8 (CT=%s, %dB): %s" % (
            hdr.get("Content-Type"), len(body), head)
    seg = None
    for ln in txt.splitlines():
        ln = ln.strip()
        if not ln or ln.startswith("#"):
            continue
        seg = ln
        break
    if not seg:
        return False, "m3u8 无分片行"
    nxt = urllib.parse.urljoin(url, seg)
    try:
        st2, hdr2, b2 = get(nxt, timeout=20, limit=4096)
    except Exception as e:
        return False, "下层取不到 d%d: %s (%s)" % (depth, e, nxt[:70])
    ct = (hdr2.get("Content-Type") or "")
    if b2[:7] == b"#EXTM3U" or "mpegurl" in ct.lower():
        return _m3u8_first_ref(nxt, depth + 1, chain + ">v")
    ok = len(b2) > 0 and (b2[:1] == b"\x47" or "mp2t" in ct or "video" in ct)
    return ok, "分片 %dB CT=%s first=0x%02x d%d" % (
        len(b2), ct or "-", b2[0] if b2 else 0, depth)


def probe_m3u8(url):
    return _m3u8_first_ref(url)


def probe_share(url):
    """非凡那种分享页：看 HTML 里有没有真 m3u8"""
    try:
        st, hdr, body = get(url, timeout=15, limit=200000)
    except Exception as e:
        return False, "分享页取不到: %s" % e
    txt = body.decode("utf-8", "ignore")
    import re
    hits = re.findall(r'https?://[^\s"\'<>\\]+\.m3u8[^\s"\'<>\\]*', txt)
    if not hits:
        return False, "分享页里无 m3u8（%d B）" % len(body)
    return probe_m3u8(hits[0].replace("\\/", "/"))


def run():
    for name, base, t in SITES:
        print("\n" + "=" * 70)
        print("### %s  %s" % (name, base))
        lst_url = base + "?ac=detail&pg=1" + (("&t=" + t) if t else "")
        try:
            st, hdr, body = get(lst_url, timeout=20)
        except Exception as e:
            print("  列表失败: %s" % e)
            continue
        print("  列表 HTTP %s  %d B" % (st, len(body)))
        try:
            j = json.loads(body.decode("utf-8", "ignore"))
        except Exception as e:
            print("  非 JSON: %s  head=%s" % (e, body[:100]))
            continue
        lst = j.get("list") or []
        print("  total=%s  本页=%d" % (j.get("total"), len(lst)))
        if not lst:
            continue
        for it in lst[:2]:
            title = it.get("vod_name")
            pu = it.get("vod_play_url") or ""
            froms = it.get("vod_play_from") or ""
            print("  -- 《%s》 from=%s" % (title, froms))
            cands = first_episode(pu)
            if not cands:
                print("     vod_play_url 空")
                continue
            for li, ep, url in cands[:9]:
                print("     线路%d %s -> %s" % (li, ep, url[:90]))
                if url.endswith(".m3u8") or ".m3u8" in url:
                    ok, desc = probe_m3u8(url)
                elif "/share/" in url:
                    ok, desc = probe_share(url)
                else:
                    # 可能是 mp4 或其他
                    try:
                        st3, h3, b3 = get(url, timeout=15, limit=1024)
                        ok = st3 == 200 and len(b3) > 0
                        desc = "HTTP %s %d B CT=%s" % (
                            st3, len(b3), h3.get("Content-Type"))
                    except Exception as e:
                        ok, desc = False, "直取失败: %s" % e
                print("       %s %s" % ("[OK]" if ok else "[NG]", desc))


if __name__ == "__main__":
    run()
