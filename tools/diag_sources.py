"""对 App 各源的真实域名做 SNI 阻断检测。

对每个域名做两次 TLS 握手：
  真 SNI → 失败 且 假 SNI → 成功   ⇒ SNI 阻断（中间设备按域名掐断）
  两次都失败                        ⇒ DNS 污染或服务器本身不可达
  两次都成功                        ⇒ 正常
"""
import socket
import ssl

DOMAINS = [
    ("红果", "hongguoduanju.com"),
    ("红果API", "api5-normal-sinfonlineb.fqnovel.com"),
    ("红果媒体", "novel.snssdk.com"),
    ("红果播放", "djapi.999888456.xyz"),
    ("七猫目录", "api-store.qmplaylet.com"),
    ("七猫详情", "api-read.qmplaylet.com"),
    ("七猫播放", "cdn-vod-playlet.wtzw.com"),
    ("韩小圈", "www.jennyhow.com"),
    ("鬼片", "guipianwu.com"),
    ("帝果", "www.dsd.com.se"),
    ("青空", "www.sorani.net"),
    ("黄果", "huangguoai.com"),
    ("黄豆", "tideember.cc"),
]

FAKE_SNI = "example.com"


def first_ip(domain, timeout=4):
    try:
        infos = socket.getaddrinfo(domain, 443, socket.AF_INET, socket.SOCK_STREAM)
        return sorted({i[4][0] for i in infos})[0]
    except Exception as e:  # noqa: BLE001
        return f"DNS失败({type(e).__name__})"


def hs(ip, sni, timeout=5):
    if not isinstance(ip, str) or ip.startswith("DNS"):
        return False, "无IP"
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.settimeout(timeout)
    try:
        s.connect((ip, 443))
    except Exception as e:  # noqa: BLE001
        return False, f"TCP {type(e).__name__}"
    ctx = ssl.create_default_context()
    ctx.check_hostname = False
    ctx.verify_mode = ssl.CERT_NONE
    try:
        w = ctx.wrap_socket(s, server_hostname=sni)
        w.close()
        return True, "OK"
    except Exception as e:  # noqa: BLE001
        return False, type(e).__name__
    finally:
        try:
            s.close()
        except Exception:  # noqa: BLE001
            pass


print(f"{'源':<10} {'域名':<30} {'IP':<16} {'真SNI':<8} {'假SNI':<8} 结论")
print("-" * 92)
rows = []
for label, dom in DOMAINS:
    ip = first_ip(dom)
    ok1, r1 = hs(ip, dom)
    ok2, r2 = hs(ip, FAKE_SNI)
    if ok1:
        verdict = "正常"
    elif ok2:
        verdict = "★ SNI 阻断"
    else:
        verdict = "不可达/污染"
    rows.append((label, dom, ip, verdict))
    print(f"{label:<10} {dom:<30} {str(ip):<16} {r1:<8} {r2:<8} {verdict}")

print()
blocked = [r[1] for r in rows if r[3].startswith("★")]
good = [r[1] for r in rows if r[3] == "正常"]
print(f"被 SNI 阻断 {len(blocked)} 个: {', '.join(blocked) or '无'}")
print(f"正常        {len(good)} 个: {', '.join(good) or '无'}")
