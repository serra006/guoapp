"""红果播放链路诊断：确认视频流落在哪个域名，以及该域名是否也被同一线路拦截。

背景：SNI 掩码只覆盖 hongguoduanju.com 一个域名。若视频流在别的 CDN 域名上，
表现为「列表能看、点播放失败」，掩码救不了，必须扩域名。

用法：
  python tools/diag_hongguo_play.py            # 自动探索一集并测所有播放域名
  python tools/diag_hongguo_play.py /player/123/456   # 指定页面路径
"""
import re
import socket
import ssl
import sys

TIMEOUT = 20
HOST = "hongguoduanju.com"
MASKED = "www.hongguoduanju.com"
IPS = ["114.80.10.219", "101.226.41.162", "182.40.130.74", "114.80.252.222"]


def _conn(ip, sni):
    ctx = ssl.create_default_context()
    ctx.check_hostname = False
    ctx.verify_mode = ssl.CERT_NONE
    raw = socket.create_connection((ip, 443), timeout=TIMEOUT)
    return ctx.wrap_socket(raw, server_hostname=sni)


def get(path, sni=MASKED, host=HOST, limit=2_000_000):
    """走掩码握手套 Host 头抓一页，返回 (状态行, 正文文本)。"""
    last = ""
    for ip in IPS:
        try:
            s = _conn(ip, sni)
        except Exception as exc:  # noqa: BLE001
            last = f"{ip} TCP/TLS 失败 {type(exc).__name__}"
            continue
        try:
            req = (f"GET {path} HTTP/1.1\r\nHost: {host}\r\n"
                   "User-Agent: Mozilla/5.0 (Linux; Android 11) AppleWebKit/537.36\r\n"
                   "Accept: text/html,application/xhtml+xml,*/*\r\n"
                   "Accept-Encoding: identity\r\nConnection: close\r\n\r\n")
            s.sendall(req.encode())
            buf = b""
            while len(buf) < limit:
                try:
                    chunk = s.recv(65536)
                except (TimeoutError, socket.timeout):
                    last = f"{ip} 读超时（已收 {len(buf)}B）"
                    break
                if not chunk:
                    break
                buf += chunk
        finally:
            try:
                s.close()
            except Exception:  # noqa: BLE001
                pass
        if not buf:
            last = f"{ip} 空响应"
            continue
        head, _, body = buf.partition(b"\r\n\r\n")
        status = head.split(b"\r\n")[0].decode(errors="replace")
        return status, body.decode("utf-8", errors="replace")
    return f"全部失败（{last}）", ""


def hosts_in(text):
    """抽出文本里出现的所有 http(s) 域名，按出现次数排序。"""
    found = re.findall(r"https?://([A-Za-z0-9.-]+\.[A-Za-z]{2,})", text)
    seen = {}
    for h in found:
        seen[h.lower()] = seen.get(h.lower(), 0) + 1
    return sorted(seen.items(), key=lambda kv: -kv[1])


def sni_check(host):
    """对目标域名的裸 SNI 做一次握手，判断这条线路是否也拦它。"""
    ip = None
    try:
        ip = socket.gethostbyname(host)
    except Exception as exc:  # noqa: BLE001
        return f"解析失败 {type(exc).__name__}", None
    out = []
    for sni, tag in ((host, "裸域名"), (MASKED, "掩码"), ("example.com", "对照")):
        try:
            s = _conn(ip, sni)
            out.append(f"{tag}OK({s.version()})")
            s.close()
        except Exception as exc:  # noqa: BLE001
            out.append(f"{tag}{type(exc).__name__}")
    return "  ".join(out), ip


def main():
    path = sys.argv[1] if len(sys.argv) > 1 else None

    if path is None:
        print("[1] 拉分类页找剧集 ID")
        status, body = get("/category/real-drama")
        print("   ", status, f"body={len(body)}B")
        if len(body) < 1000:
            print("    页面异常，内容片段：", body[:200])
            return
        pats = {
            "/player/x/y": r"/player/(\d+)/(\d+)",
            "series_id": r'"series_id"\s*:\s*"?(\d+)',
            "seriesId": r'"seriesId"\s*:\s*"?(\d+)',
            "book_id": r'"book_id"\s*:\s*"?(\d+)',
            "video_id": r'"video_id"\s*:\s*"?(\d+)',
        }
        ids = {}
        for name, pat in pats.items():
            m = re.findall(pat, body)
            ids[name] = m[:5]
            print(f"    {name:12} {len(m):4d} 个  样例 {m[:3]}")
        pair = re.findall(r"/player/(\d+)/(\d+)", body)
        if pair:
            path = f"/player/{pair[0][0]}/{pair[0][1]}"
        else:
            sid = (ids.get("series_id") or ids.get("seriesId") or ids.get("book_id") or [None])[0]
            vid = (ids.get("video_id") or [None])[0]
            if not sid:
                print("\n    分类页里没有可用 ID，请手动传一个 /player/<剧ID>/<集ID>：")
                print("    电脑浏览器打开 hongguoduanju.com，随便点一部剧，把地址栏路径发我。")
                return
            path = f"/player/{sid}/{vid or 1}"

    print(f"\n[2] 拉播放页 {path}")
    status, body = get(path)
    print("   ", status, f"body={len(body)}B")
    if len(body) < 500:
        print("    内容片段：", body[:300])
        return

    print("\n[3] 页面里出现的域名（按次数）")
    hosts = hosts_in(body)
    for h, n in hosts[:15]:
        print(f"    {n:5d}  {h}")

    key_hosts = [h for h, _ in hosts if h != HOST and h != MASKED]
    print("\n[4] 对播放相关域名做 SNI 对照（裸域名 / 掩码 / 对照）")
    for h in key_hosts[:8]:
        line, ip = sni_check(h)
        print(f"    {h:38} {ip or '-':16} {line}")

    print("\n[5] video_player_info 片段")
    idx = body.find("video_player_info")
    if idx >= 0:
        print("   ", body[max(0, idx - 120): idx + 600].replace("\n", " ")[:700])
    else:
        print("    页面里没有 video_player_info（可能需要登录或该页是壳）")


if __name__ == "__main__":
    main()
