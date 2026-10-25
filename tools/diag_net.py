"""诊断：DNS 解析差异 + 目标 IP 的 TCP/TLS 行为。"""
import socket
import ssl
import struct
import sys


def dns_query(domain, server, timeout=4):
    """手搓一个 A 记录查询，绕过系统 DNS 缓存。"""
    tid = 0x1234
    q = struct.pack(">HHHHHH", tid, 0x0100, 1, 0, 0, 0)
    for part in domain.split("."):
        q += bytes([len(part)]) + part.encode()
    q += b"\x00" + struct.pack(">HH", 1, 1)
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    s.settimeout(timeout)
    try:
        s.sendto(q, (server, 53))
        data, _ = s.recvfrom(2048)
    except Exception as e:  # noqa: BLE001
        return [f"查询失败 {type(e).__name__}"]
    finally:
        s.close()
    # 跳过 header(12) + question
    i = 12
    while data[i] != 0:
        i += data[i] + 1
    i += 5
    ancount = struct.unpack(">H", data[6:8])[0]
    ips = []
    for _ in range(ancount):
        if data[i] & 0xC0 == 0xC0:
            i += 2
        else:
            while data[i] != 0:
                i += data[i] + 1
            i += 1
        rtype, _rclass, _ttl, rdlen = struct.unpack(">HHIH", data[i:i + 10])
        i += 10
        if rtype == 1 and rdlen == 4:
            ips.append(socket.inet_ntoa(data[i:i + 4]))
        i += rdlen
    return ips


def tls_probe(ip, sni, timeout=6):
    try:
        raw = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        raw.settimeout(timeout)
        raw.connect((ip, 443))
    except Exception as e:  # noqa: BLE001
        return f"TCP 失败 {type(e).__name__}"
    ctx = ssl.create_default_context()
    ctx.check_hostname = False
    ctx.verify_mode = ssl.CERT_NONE
    try:
        s = ctx.wrap_socket(raw, server_hostname=sni)
        der = s.getpeercert(binary_form=True) or b""
        ver = s.version()
        s.close()
        return f"TLS OK {ver} cert={len(der)}B"
    except ssl.SSLError as e:
        return f"TLS 失败 {e.reason or e}"
    except Exception as e:  # noqa: BLE001
        return f"握手期 RST/中断 {type(e).__name__}"
    finally:
        try:
            raw.close()
        except Exception:  # noqa: BLE001
            pass


DOMAINS = ["hongguoduanju.com", "v.fengbao14.com"]
SERVERS = [
    ("223.5.5.5", "阿里 DNS"),
    ("119.29.29.29", "腾讯 DNS"),
    ("114.114.114.114", "114 DNS"),
    ("8.8.8.8", "Google（国内会被污染）"),
    ("1.1.1.1", "Cloudflare（国内会被污染）"),
]

for dom in DOMAINS:
    print(f"\n{'=' * 58}\n{dom}")
    results = {}
    for ip, name in SERVERS:
        ips = dns_query(dom, ip)
        results[name] = ips
        print(f"  {name:22} {ips}")
    print(f"  {'系统默认':22} {sorted({r[4][0] for r in socket.getaddrinfo(dom, 443, socket.AF_INET)})[:6]}")

print(f"\n{'=' * 58}\n对具体 IP 做 TLS 探测（SNI 同域名）")
for ip in ("101.226.41.162", "101.226.41.163", "114.80.10.219",
           "47.97.226.443", "37.87.226.443", "37.77.82.138"):
    print(f"  {ip:16} -> {tls_probe(ip, 'hongguoduanju.com')}")
