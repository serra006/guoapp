import base64, sys, os
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import gh_api as api

paths = [
    "pubspec.yaml",
    "versions.json",
    "native/core/provider_hongguo_register.go",
    "native/core/provider_hongguo_playback_sign.go",
    "native/core/provider_hongguo_native_media.go",
    "native/core/provider_hongguo_v5_sign.go",
    "native/core/provider_hongguo_app.go",
]
for p in paths:
    try:
        r = api.get(api.repo_path(f"/contents/{p}"))
        raw = base64.b64decode(r["content"])
        local = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), p.replace("/", os.sep))
        same = "?"
        if os.path.isfile(local):
            with open(local, "rb") as f:
                lb = f.read()
            same = "SAME" if lb.replace(b"\r\n", b"\n") == raw.replace(b"\r\n", b"\n") else f"DIFF(local={len(lb)}B remote={len(raw)}B)"
        print(f"{p:55s} remote={len(raw):7d}B  {same}")
    except Exception as e:
        print(f"{p:55s} ERROR {type(e).__name__} {e}")
