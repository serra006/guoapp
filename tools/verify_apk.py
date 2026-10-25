"""校验 APK 交付物：包名 / versionName / versionCode / 签名 / ABI。

用法：
  python tools/verify_apk.py E:/6/apk77
  python tools/verify_apk.py E:/6/apk77/a.apk b.apk
"""
import glob
import os
import re
import subprocess
import sys
import zipfile

BT = r"C:\Users\se\AppData\Local\Android\Sdk\build-tools\36.1.0"
AAPT2 = os.path.join(BT, "aapt2.exe")
APKSIGNER = os.path.join(BT, "apksigner.bat")


def badging(apk):
    out = subprocess.run([AAPT2, "dump", "badging", apk], capture_output=True, text=True)
    if out.returncode != 0:
        return None, out.stderr[:200]
    txt = out.stdout
    pkg = re.search(r"package: name='([^']+)' versionCode='(\d+)' versionName='([^']+)'", txt)
    abis = sorted(set(re.findall(r"native-code: (.+)", txt)))
    return {
        "package": pkg.group(1) if pkg else "?",
        "versionCode": int(pkg.group(2)) if pkg else 0,
        "versionName": pkg.group(3) if pkg else "?",
        "abis": " ".join(a.strip("'") for a in abis) if abis else "?",
        "sdk": (re.search(r"sdkVersion:'(\d+)'", txt) or [None, "?"])[1],
        "minsdk": (re.search(r"minSdkVersion:'(\d+)'", txt) or [None, "?"])[1],
        "label": (re.search(r"application-label:'([^']*)'", txt) or [None, "?"])[1],
    }, None


def signature(apk):
    out = subprocess.run([APKSIGNER, "verify", "--print-certs", apk],
                         capture_output=True, text=True)
    txt = out.stdout + out.stderr
    if out.returncode != 0:
        return "验签失败: " + txt.strip().splitlines()[-1][:120] if txt.strip() else "验签失败"
    dt = re.search(r"certificate DN: (.+)", txt)
    sha = re.search(r"certificate SHA-256 digest: ([0-9a-f]+)", txt)
    scheme = "v2+" if "v2" in txt or "Verified using v2" in txt else "v1"
    return f"{scheme} | {(dt.group(1).strip() if dt else '?')} | sha256:{sha.group(1)[:16] if sha else '?'}"


def natlibs(apk):
    with zipfile.ZipFile(apk) as z:
        libs = sorted({n.split("/")[1] for n in z.namelist() if n.startswith("lib/")})
    return ",".join(libs) if libs else "无"


def main():
    args = sys.argv[1:]
    if not args:
        raise SystemExit("用法: python tools/verify_apk.py <目录或APK...>")
    apks = []
    for a in args:
        if os.path.isdir(a):
            apks.extend(sorted(glob.glob(os.path.join(a, "*.apk"))))
        elif a.lower().endswith(".apk"):
            apks.append(a)
    if not apks:
        raise SystemExit("没找到 APK")

    print(f"{'文件':46} {'包名':18} {'版本':16} {'vCode':>6} {'ABI':22} 签名")
    print("-" * 150)
    for apk in apks:
        info, err = badging(apk)
        if err:
            print(f"{os.path.basename(apk):46} 解析失败 {err}")
            continue
        sig = signature(apk)
        print(f"{os.path.basename(apk):46} {info['package']:18} "
              f"{info['versionName']:16} {info['versionCode']:>6} {info['abis']:22} {sig}")
        print(f"{'':46} label={info['label']}  minSdk={info['minsdk']}  targetSdk={info['sdk']}  "
              f"libs={natlibs(apk)}")


if __name__ == "__main__":
    main()
