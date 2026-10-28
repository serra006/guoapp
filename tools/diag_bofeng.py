#!/usr/bin/env python3
"""暴风资源（苹果CMS v10 采集接口）巡检。

用途：站源某天不出内容时，一键分清是「接口挂了」「搜索变了」「m3u8 拿不到」
还是「分片播不了」，而不是瞎猜。共四层，每层独立判定。

用法：
    python tools/diag_bofeng.py                 # 全量巡检
    python tools/diag_bofeng.py 闪婚 战神        # 指定关键词
"""

import json
import ssl
import sys
import urllib.error
import urllib.parse
import urllib.request

BASE = "https://bfzyapi.com/api.php/provide/vod/"
UA = {
    "User-Agent": "Mozilla/5.0 (Linux; Android 13) AppleWebKit/537.36",
}
CTX = ssl.create_default_context()
# 本机代理偶尔会改写证书链，巡检只关心内容是否可取，不做证书校验。
CTX.check_hostname = False
CTX.verify_mode = ssl.CERT_NONE


def fetch(url, timeout=15, limit=900000):
    """取回字节。url 里可能带中文，先做编码，否则 urllib 会抛 UnicodeEncodeError。"""
    safe = urllib.parse.quote(url, safe=":/?#[]@!$&'()*+,;=%")
    request = urllib.request.Request(safe, headers=UA)
    with urllib.request.urlopen(request, timeout=timeout, context=CTX) as response:
        return response.read(limit), dict(response.headers), response.geturl()


def api(**params):
    url = BASE + "?" + urllib.parse.urlencode(params)
    body, _, _ = fetch(url)
    return json.loads(body.decode("utf-8", "replace"))


def check_catalog():
    """第 1 层：接口是否活着、短剧分类是否还在。"""
    try:
        data = api(ac="list")
    except Exception as exc:  # noqa: BLE001
        print(f"  [失败] 分类接口不可达：{type(exc).__name__} {exc}")
        return False
    classes = data.get("class") or []
    short = [c for c in classes if "短剧" in str(c.get("type_name"))]
    print(f"  [OK] 分类 {len(classes)} 个，短剧类 {len(short)} 个")
    for item in short:
        print(f"       type_id={item.get('type_id')}  {item.get('type_name')}")
    if not short:
        print("  [警告] 找不到「短剧」分类，站点可能改版")
    return bool(short)


def check_search(keywords):
    """第 2 层：搜索是否还有结果、分页是否正常。"""
    ok = True
    for keyword in keywords:
        try:
            data = api(ac="detail", wd=keyword, pg=1)
        except Exception as exc:  # noqa: BLE001
            print(f"  [失败] 搜索「{keyword}」：{type(exc).__name__} {exc}")
            ok = False
            continue
        rows = data.get("list") or []
        total = data.get("total")
        names = [str(r.get("vod_name"))[:16] for r in rows[:3]]
        print(f"  [OK] 「{keyword}」返回 {len(rows)} 条 / 总 {total}  {names}")
        if not rows:
            print(f"  [警告] 「{keyword}」0 条，可能关键词不对或站点调整")
            ok = False
    return ok


def check_media(keyword):
    """第 3、4 层：能否拿到 m3u8、分片是否可下、是否加密。"""
    try:
        data = api(ac="detail", wd=keyword)
    except Exception as exc:  # noqa: BLE001
        print(f"  [失败] 详情不可达：{type(exc).__name__} {exc}")
        return False
    rows = data.get("list") or []
    if not rows:
        print(f"  [警告] 没有可用于验证播放的条目（{keyword}）")
        return False

    item = rows[0]
    name = str(item.get("vod_name"))
    play_url = str(item.get("vod_play_url") or "")
    parts = [p for p in play_url.split("#") if "$" in p]
    print(f"  片名《{name}》 声明集数 {len(parts)}  备注 {item.get('vod_remarks')}  年份 {item.get('vod_year')}")

    try:
        first_name, media_url = parts[0].split("$", 1)
    except Exception:  # noqa: BLE001
        print(f"  [失败] 播放串格式异常：{play_url[:120]}")
        return False

    try:
        body, headers, final = fetch(media_url, limit=200000)
    except Exception as exc:  # noqa: BLE001
        print(f"  [失败] m3u8 取不到：{type(exc).__name__} {exc}")
        return False

    text = body.decode("utf-8", "replace")
    if not text.lstrip().startswith("#EXTM3U"):
        print(f"  [失败] 不是 m3u8（多为分享页 HTML）：CT={headers.get('Content-Type')} 前80字={text[:80]!r}")
        return False

    lines = [l.strip() for l in text.splitlines() if l.strip()]
    segments = [l for l in lines if not l.startswith("#")]
    keys = [l for l in lines if l.startswith("#EXT-X-KEY")]
    durations = []
    for line in lines:
        if line.startswith("#EXTINF:"):
            try:
                durations.append(float(line[8:].rstrip(",").split(",")[0]))
            except ValueError:
                pass
    total = sum(durations)
    print(f"  [OK] m3u8 {len(body)}B / {len(segments)} 分片 / 总时长约 {total/60:.1f} 分钟")
    print(f"       集名 {first_name}  首条 {media_url[:88]}")
    print(f"       加密 {'有 ' + keys[0][:60] if keys else '无（明文）'}")

    # 整剧单流判定：只有 1 条播放地址、但时长远超单集
    if len(parts) == 1 and total > 8 * 60:
        print(f"  [注意] 整剧合成单流：声明 1 集，实际 {total/60:.1f} 分钟 —— 选集必须靠切片")

    if segments:
        seg = segments[0]
        if not seg.startswith("http"):
            seg = final.rsplit("/", 1)[0] + "/" + seg.lstrip("/")
        try:
            chunk, seg_headers, _ = fetch(seg, limit=2048)
            sync = chunk[0] if chunk else 0
            good = sync == 0x47
            print(f"  [{'OK' if good else '警告'}] 分片 {len(chunk)}B CT={seg_headers.get('Content-Type')} 首字节 0x{sync:02x}"
                  f"{'（TS 正常）' if good else '（非 TS 同步字节，可能换了封装）'}")
            if not good:
                return False
        except Exception as exc:  # noqa: BLE001
            print(f"  [失败] 分片下不来：{type(exc).__name__} {exc}")
            return False
    return True


def main():
    keywords = sys.argv[1:] or ["闪婚", "战神", "总裁"]
    print("=" * 64)
    print("暴风资源巡检  ", BASE)
    print("=" * 64)

    print("\n[1/4] 分类接口")
    check_catalog()

    print(f"\n[2/4] 搜索（{', '.join(keywords)}）")
    check_search(keywords)

    print(f"\n[3-4/4] m3u8 与分片（用「{keywords[0]}」的第一条验证）")
    playable = check_media(keywords[0])

    print("\n" + "=" * 64)
    print("结论：" + ("全链路可用" if playable else "存在断点，看上面的 [失败]/[警告] 行"))
    print("=" * 64)
    return 0 if playable else 1


if __name__ == "__main__":
    sys.exit(main())
