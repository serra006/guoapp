"""api.github.com 的最小客户端：多传输通道回退 + 自动带 token。

本机到 github.com 的 TLS 常被拦（schannel handshake / OpenSSL eof），
但 api.github.com 通常可用。三个通道任一可能单独失效：

1. 直连
2. 系统代理 127.0.0.1:20808（v2rayN/xray）
3. WorkBuddy 内置代理 127.0.0.1:58366（会随会话变化，经常不在）

调用方只管 `api.get(path)` / `api.post(path, payload)`，不用关心走哪条路。
token 从 origin 的 remote url 里取，避免额外文件泄露密钥。
"""
import json
import re
import subprocess
import urllib.error
import urllib.request

OWNER, REPO = "serra006", "guoapp"
API = "https://api.github.com"
ROOT = r"E:\6\guoapp"

_TRANSPORT_NAMES = (
    "direct",
    "proxy20808",
    "proxy58366",
)
_PROXY_MAP = {
    "direct": {},
    "proxy20808": {"http": "http://127.0.0.1:20808", "https": "http://127.0.0.1:20808"},
    "proxy58366": {"http": "http://127.0.0.1:58366", "https": "http://127.0.0.1:58366"},
}
_OPENERS = [
    (name, urllib.request.build_opener(urllib.request.ProxyHandler(_PROXY_MAP[name])))
    for name in _TRANSPORT_NAMES
]
_state = {"name": None, "opener": None}


def token():
    url = subprocess.run(
        ["git", "remote", "get-url", "origin"],
        cwd=ROOT,
        capture_output=True,
        text=True,
        check=True,
    ).stdout.strip()
    match = re.search(r"https://[^:]+:([^@]+)@", url)
    if not match:
        raise SystemExit("remote url 里没有 token")
    return match.group(1)


TOKEN = token()


def call(method, path, payload=None, accept="application/vnd.github+json", raw=False):
    """发一个 API 请求。raw=True 时返回 Response 对象（用于下载）。"""
    data = json.dumps(payload).encode() if payload is not None else None
    candidates = (
        [(_state["name"], _state["opener"])] if _state["opener"] else []
    ) + [pair for pair in _OPENERS if pair[0] != _state["name"]]
    errors = []
    for name, opener in candidates:
        req = urllib.request.Request(API + path, data=data, method=method)
        req.add_header("Authorization", "Bearer " + TOKEN)
        req.add_header("Accept", accept)
        req.add_header("X-GitHub-Api-Version", "2022-11-28")
        req.add_header("User-Agent", "curl/8.5.0")
        if data:
            req.add_header("Content-Type", "application/json")
        try:
            response = opener.open(req, timeout=120)
            if raw:
                _remember(name, opener)
                return response
            with response as r:
                body = r.read().decode()
            _remember(name, opener)
            return json.loads(body) if body else {}
        except urllib.error.HTTPError as exc:
            # 4xx/5xx 是「服务端已经答复」，说明链路没问题，直接抛出真实原因
            detail = ""
            try:
                detail = exc.read().decode()[:300]
            except Exception:  # noqa: BLE001
                pass
            raise SystemExit(f"GitHub 拒绝 {method} {path}: HTTP {exc.code} {detail}")
        except Exception as exc:  # noqa: BLE001
            errors.append(f"{name}: {type(exc).__name__}: {exc}")
    raise SystemExit("api.github.com 所有通道都失败:\n  " + "\n  ".join(errors))


def _remember(name, opener):
    if _state["name"] != name:
        _state["name"], _state["opener"] = name, opener


def get(path, **kwargs):
    return call("GET", path, **kwargs)


def post(path, payload=None, **kwargs):
    return call("POST", path, payload, **kwargs)


def patch(path, payload=None, **kwargs):
    return call("PATCH", path, payload, **kwargs)


def put(path, payload=None, **kwargs):
    return call("PUT", path, payload, **kwargs)


def repo_path(suffix=""):
    return f"/repos/{OWNER}/{REPO}{suffix}"
