"""在手机播放失败前抢抓本地代理的原始字节流。

背景：播放器打开代理地址后约 3 秒就报错并立即释放会话，之后再请求就是 410。
所以必须在 logcat 出现地址的瞬间并发抓取。

用法：
    python tools/grab_proxy.py --seconds 1200 --out E:/6/hg_proxy
"""

import argparse
import hashlib
import http.client
import os
import re as _re
import subprocess
import threading
import time

ADB = r"C:\Users\se\AppData\Local\Android\Sdk\platform-tools\adb.exe"
RE = _re.compile(r"http://127\.0\.0\.1:(\d+)/([0-9a-f]{48})/([0-9a-f]{24})(\.(?:mp4|m3u8|ts|m4s))")
LOCK = threading.Lock()
SEEN = set()
FORWARDED = {"port": None}

# 先抢 1MB 头（含 moov 与第一段 mdat），再抢 64KB 头（最轻），最后抢整份。
PLANS = [
    ("head1m", "bytes=0-1048575"),
    ("head64k", "bytes=0-65535"),
    ("full", None),
]


def fetch(host_port, token, name, tag, rng, out, dev_port):
    dest = os.path.join(out, f"{name[:8]}.{tag}.bin")
    hdrs = {"User-Agent": "grab/1.0"}
    if rng:
        hdrs["Range"] = rng
    got = 0
    meta = ""
    try:
        conn = http.client.HTTPConnection("127.0.0.1", host_port, timeout=25)
        conn.request("GET", f"/{token}/{name}", headers=hdrs)
        resp = conn.getresponse()
        meta = (f"[dev:{dev_port}] {resp.status} CL={resp.getheader('Content-Length')} "
                f"CR={resp.getheader('Content-Range')} CT={resp.getheader('Content-Type')}")
        if resp.status != 200 and resp.status != 206:
            body = resp.read(400)
            print(f"   [{tag:7}] {meta} 错误体={body!r}", flush=True)
            conn.close()
            return
        with open(dest, "wb") as fh:
            while True:
                chunk = resp.read(262144)
                if not chunk:
                    break
                fh.write(chunk)
                got += len(chunk)
        conn.close()
    except Exception as exc:  # noqa: BLE001
        code = getattr(exc, "code", "")
        meta += f" | 中断 {type(exc).__name__}{('/' + str(code)) if code else ''}"
    if got:
        data = open(dest, "rb").read()
        sha = hashlib.sha256(data).hexdigest()[:16]
        head = data[:64]
        print(f"   [{tag:7}] {meta} got={got} sha={sha} 开头={head!r}", flush=True)
    else:
        print(f"   [{tag:7}] {meta} 无数据", flush=True)


def ensure_forward(device, host_port, dev_port):
    if FORWARDED["port"] == dev_port:
        return
    subprocess.run([ADB, "-s", device, "forward", "--remove", f"tcp:{host_port}"],
                   capture_output=True)
    res = subprocess.run([ADB, "-s", device, "forward", f"tcp:{host_port}", f"tcp:{dev_port}"],
                         capture_output=True, text=True)
    FORWARDED["port"] = dev_port if res.returncode == 0 else None
    print(f"转发 tcp:{host_port} -> 设备 tcp:{dev_port} "
          f"{'就绪' if res.returncode == 0 else '失败:' + res.stderr.strip()}", flush=True)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--device", default="192.168.3.24:37205")
    ap.add_argument("--host-port", type=int, default=18080)
    ap.add_argument("--seconds", type=int, default=600)
    ap.add_argument("--out", default="E:/6/hg_proxy")
    args = ap.parse_args()
    os.makedirs(args.out, exist_ok=True)

    subprocess.run([ADB, "connect", args.device], capture_output=True)
    print(f"监听中（{args.seconds}s），等待播放…", flush=True)

    proc = None
    deadline = time.time() + args.seconds
    try:
        while time.time() < deadline:
            if proc is None or proc.poll() is not None:
                subprocess.run([ADB, "connect", args.device], capture_output=True)
                proc = subprocess.Popen([ADB, "-s", args.device, "logcat", "-v", "time", "-T", "1"],
                                        stdout=subprocess.PIPE, text=True, errors="replace", bufsize=1)
                print("(重新)挂上 logcat 监视", flush=True)
            line = proc.stdout.readline()
            if not line:
                time.sleep(0.01)
                continue
            m = RE.search(line)
            if not m:
                continue
            dev_port, token, name = int(m.group(1)), m.group(2), m.group(3) + m.group(4)
            with LOCK:
                key = token + name
                if key in SEEN:
                    continue
                SEEN.add(key)
            print(f"\n>>> 命中 dev:{dev_port} {token[:8]}…/{name} 立即抓取", flush=True)
            ensure_forward(args.device, args.host_port, dev_port)
            for tag, rng in PLANS:
                threading.Thread(
                    target=fetch,
                    args=(args.host_port, token, name, tag, rng, args.out, dev_port),
                    daemon=True,
                ).start()
    finally:
        if proc:
            proc.kill()
    print(f"\n结束，共命中 {len(SEEN)} 个会话", flush=True)


if __name__ == "__main__":
    main()
