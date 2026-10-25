"""判定「裸域名 SNI 被 RST」的对照实验 + 修复有效性验证。

对同一批真实 IP 分别用三种 SNI 握手：
  1. hongguoduanju.com      （被封的裸域名，App 旧版行为）
  2. www.hongguoduanju.com  （新版掩码行为）
  3. example.com            （无关域名，对照组）
并顺带查 AAAA（看是否 IPv6 可用），以及带 Host 头拉一次 HTML 看内容对不对。
"""
import socket
import ssl
import struct
import sys

TIMEOUT = 6


def resolve(domain, server, tcp=False, qtype=1):
    """qtype 1=A, 28=AAAA。tcp=True 走 TCP 53（绕 UDP 劫持）。"""
    tid = 0x1234
    q = struct.pack(">HHHHHH", tid, 0x0100, 1, 0, 0, 0)
    for part in domain.split("."):
        q += bytes([len(part)]) + part.encode()
    q += b"\x00" + struct.pack(">HH", qtype, 1)

    if tcp:
        s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        s.settimeout(TIMEOUT)
        try:
            s.connect((server, 53))
            s.sendall(struct.pack(">H", len(q)) + q)
            ln = struct.unpack(">H", s.recv(2))[0]
            data = b""
            while len(data) < ln:
                chunk = s.recv(ln - len(data))
                if not chunk:
                    break
                data += chunk
        except Exception as e:  # noqa: BLE001
            return [f"失败 {type(e).__name__}"]
        finally:
            s.close()
    else:
        s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        s.settimeout(TIMEOUT)
        try:
            s.sendto(q, (server, 53))
            data, _ = s.recvfrom(4096)
        except Exception as e:  # noqa: BLE001
            return [f"失败 {type(e).__name__}"]
        finally:
            s.close()

    i = 12
    while data[i] != 0:
        i += data[i] + 1
    i += 5
    ancount = struct.unpack(">H", data[6:8])[0]
    out = []
    for _ in range(ancount):
        if data[i] & 0xC0 == 0xC0:
            i += 2
        else:
            while data[i] != 0:
                i += data[i] + 1
            i += 1
        rtype, _cls, _ttl, rdlen = struct.unpack(">HHIH", data[i:i + 10])
        i += 10
        raw = data[i:i + rdlen]
        if rtype == 1 and rdlen == 4:
            out.append(socket.inet_ntoa(raw))
        elif rtype == 28 and rdlen == 16:
            out.append(socket.inet_ntop(socket.AF_INET6, raw))
        i += rdlen
    return out


def tls_status(ip, sni):
    """返回 (状态字符串, 是否可用)。"""
    try:
        raw = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        raw.settimeout(TIMEOUT)
        raw.connect((ip, 443))
    except Exception as e:  # noqa: BLE001
        return f"TCP 连不上 {type(e).__name__}", False
    ctx = ssl.create_default_context()
    ctx.check_hostname = False
    ctx.verify_mode = ssl.CERT_NONE
    try:
        s = ctx.wrap_socket(raw, server_hostname=sni)
        ver = s.version()
        cert = s.getpeercert(binary_form=True) or b""
        s.close()
        return f"TLS OK {ver} cert={len(cert)}B", True
    except Exception as e:  # noqa: BLE001
        return f"RST/中断 {type(e).__name__}", False
    finally:
        try:
            raw.close()
        except Exception:  # noqa: BLE001
            pass


def fetch(ip, sni, host, path="/"):
    """用给定 SNI + Host 头抓一次 HTML，确认拿到的是不是目标站。"""
    ctx = ssl.create_default_context()
    ctx.check_hostname = False
    ctx.verify_mode = ssl.CERT_NONE
    try:
        raw = socket.create_connection((ip, 443), timeout=TIMEOUT)
        s = ctx.wrap_socket(raw, server_hostname=sni)
        req = (f"GET {path} HTTP/1.1\r\nHost: {host}\r\n"
               f"User-Agent: Mozilla/5.0\r\nAccept: */*\r\nConnection: close\r\n\r\n")
        s.sendall(req.encode())
        buf = b""
        while len(buf) < 262144:
            b = s.recv(65536)
            if not b:
                break
            buf += b
        s.close()
        head = buf.split(b"\r\n\r\n", 1)[0].decode(errors="replace").splitlines()[0]
        body = buf.split(b"\r\n\r\n", 1)[1] if b"\r\n\r\n" in buf else b""
        mark = ""
        for tag in (b"8662", b"hongguo", b"aid"):
            if tag in body:
                mark += f" 含{tag.decode()} "
        return f"{head} body={len(body)}B{mark}"
    except Exception as e:  # noqa: BLE001
        return f"失败 {type(e).__name__}: {e}"


DOMAIN = "hongguoduanju.com"
MASKED = "www." + DOMAIN

print("=" * 74)
print("[1] DNS：UDP53 与 TCP53 对照（TCP 可绕开 UDP 劫持）")
print("=" * 74)
for qtype, label in ((1, "A"), (28, "AAAA")):
    u = resolve(DOMAIN, "223.5.5.5", tcp=False, qtype=qtype)
    t = resolve(DOMAIN, "223.5.5.5", tcp=True, qtype=qtype)
    print(f"  {label:5} UDP 223.5.5.5 : {u}")
    print(f"  {label:5} TCP 223.5.5.5 : {t}")

print()
print("=" * 74)
print("[2] 本机是否有 IPv6（能上 v6 的话电视/手机可能走 v6 就绕开了 v4 阻断）")
print("=" * 74)
try:
    v6 = resolve(DOMAIN, "223.5.5.5", tcp=True, qtype=28)
    print(f"  AAAA 记录: {v6}")
    for a in v6:
        if ":" in a:
            try:
                raw = socket.socket(socket.AF_INET6, socket.SOCK_STREAM)
                raw.settimeout(TIMEOUT)
                raw.connect((a, 443))
                print(f"  IPv6 {a} TCP 建连失败？否 -> 可连")
                raw.close()
            except Exception as e:  # noqa: BLE001
                print(f"  IPv6 {a} TCP: {type(e).__name__} {e}")
except Exception as e:  # noqa: BLE001
    print(f"  {type(e).__name__}: {e}")

print()
print("=" * 74)
print("[3] 同 IP 换 SNI 对照（核心）")
print("=" * 74)
ips = resolve(DOMAIN, "8.8.8.8", tcp=True) or []
ips = [i for i in ips if "." in i][:4]
if not ips:
    ips = ["101.226.41.162", "114.80.10.219"]
print(f"  测试 IP: {ips}")
for ip in ips:
    plain, _ = tls_status(ip, DOMAIN)
    masked, _ = tls_status(ip, MASKED)
    other, _ = tls_status(ip, "example.com")
    print(f"  {ip:16} 裸域名 {plain:34} | www 掩码 {masked:34} | 无关SNI {other}")

print()
print("=" * 74)
print("[4] 用掩码 SNI 真拉一次页面（确认拿到的确实是红果）")
print("=" * 74)
for ip in ips[:2]:
    print(f"  {ip:16} {fetch(ip, MASKED, DOMAIN, '/')}")
