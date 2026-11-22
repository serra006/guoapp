#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""苹果CMS 候选源：分类表 + 短剧库存 + AES 关键帧检查"""
import json
import ssl
import urllib.parse
import urllib.request
import re

CTX = ssl.create_default_context()
CTX.check_hostname = False
CTX.verify_mode = ssl.CERT_NONE
UA = ("Mozilla/5.0 (Linux; Android 12; PGT-AN10) AppleWebKit/537.36 "
      "(KHTML, like Gecko) Chrome/120.0.0.0 Mobile Safari/537.36")
OPENER = urllib.request.build_opener(urllib.request.HTTPSHandler(context=CTX))


def q(u):
    return urllib.parse.quote(u, safe=":/?#[]@!$&'()*+,;=%")


def get(u, timeout=20, limit=None):
    req = urllib.request.Request(q(u), headers={"User-Agent": UA})
    with OPENER.open(req, timeout=timeout) as r:
        return r.status, dict(r.headers), (r.read() if limit is None else r.read(limit))


BASES = [
    ("魔都", "https://www.mdzyapi.com/api.php/provide/vod/"),
    ("360", "https://360zy.com/api.php/provide/vod/"),
    ("艾旦", "https://lovedan.net/api.php/provide/vod/"),
    ("麒麟", "https://www.qlys.cc/api.php/provide/vod/"),
    ("非凡", "https://ffzy5.tv/api.php/provide/vod/"),
]

KEYWORDS = ("短剧", "爽文", "微短剧", "小剧场", "古装", "霸总", "穿越", "重生", "甜宠")

for name, base in BASES:
    print("\n" + "=" * 66)
    print("### %s" % name)
    try:
        st, h, b = get(base + "?ac=list", timeout=20)
        j = json.loads(b.decode("utf-8", "ignore"))
    except Exception as e:
        print("  分类失败: %s" % e)
        continue
    cls = j.get("class") or []
    hits = []
    for c in cls:
        if c.get("type_pid") != 0:
            continue
        label = "%s(%s)" % (c.get("type_name"), c.get("type_id"))
        mark = "  <<<" if any(k in (c.get("type_name") or "") for k in KEYWORDS) else ""
        print("  %s%s" % (label, mark))
        if mark:
            hits.append((c.get("type_id"), c.get("type_name")))
    # 对每个疑似短剧分类探 total
    for tid, tname in hits[:6]:
        try:
            st, h, b = get("%s?ac=detail&pg=1&t=%s" % (base, tid), timeout=25, limit=4000)
            head = b.decode("utf-8", "ignore")
            m = re.search(r'"total"\s*:\s*(\d+)', head)
            print("    -> t=%s %s total=%s" % (tid, tname, m.group(1) if m else "?"))
        except Exception as e:
            print("    -> t=%s %s 失败 %s" % (tid, tname, e))

# 360 的 AES 检查
print("\n" + "=" * 66)
print("### 360 AES-128 检查")
try:
    st, h, b = get("https://360zy.com/api.php/provide/vod/?ac=detail&pg=1&t=46", timeout=25)
    j = json.loads(b.decode("utf-8", "ignore"))
    it = (j.get("list") or [])[0]
    url = it["vod_play_url"].split("#")[0].split("$")[-1]
    print("  样本:", it.get("vod_name"), url)
    st, h, b = get(url, timeout=20, limit=4096)
    txt = b.decode("utf-8", "ignore")
    while "#EXTM3U" in txt and "#EXTINF" not in txt:
        seg = [l for l in txt.splitlines() if l and not l.startswith("#")][0]
        url = urllib.parse.urljoin(url, seg)
        st, h, b = get(url, timeout=20, limit=4096)
        txt = b.decode("utf-8", "ignore")
    for ln in txt.splitlines():
        if ln.startswith("#EXT-X-KEY"):
            print("  ", ln)
            m = re.search(r'URI="([^"]+)"', ln)
            if m:
                ku = urllib.parse.urljoin(url, m.group(1))
                try:
                    st, h, kb = get(ku, timeout=20)
                    print("   key: HTTP %s  %d B  %s" % (st, len(kb), kb.hex()))
                except Exception as e:
                    print("   key 取不到:", e)
    print("  明文?", "无 EXT-X-KEY" if "EXT-X-KEY" not in txt else "有加密")
except Exception as e:
    print("  失败:", e)
