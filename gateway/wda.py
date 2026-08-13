import json, os, pathlib, shutil, signal, subprocess, time
from . import config as _config

WHATSAPP_BUNDLE_ID = "net.whatsapp.WhatsApp"
# WhatsApp 界面元素选择器（与平台 internal/wda/whatsapp.go 对齐；真机联调时按需覆盖）。
MESSAGE_INPUT_SELECTOR = ("class chain", "**/XCUIElementTypeTextView[1]")
SEND_BUTTON_SELECTORS = [
    ("accessibility id", "Send"),
    ("accessibility id", "send"),
    ("predicate string", "name == 'Send'"),
    ("predicate string", "name == '发送'"),
]


class WDAManager:
    """按 UDID 激活/停止/健康检查 WDA。多台手机各自独立端口与进程。"""

    def __init__(self):
        self.processes: dict[str, subprocess.Popen] = {}   # udid -> xcodebuild 进程
        self._xctestrun: str | None = None

    # ---------- 构建一次，产物复用 ----------
    def ensure_built(self) -> str:
        if self._xctestrun and pathlib.Path(self._xctestrun).exists():
            return self._xctestrun
        cfg = _config.config
        derived = cfg.derived_data
        cmd = [
            "xcodebuild", "-project", str(cfg.project_root / "WebDriverAgent.xcodeproj"),
            "-scheme", "WebDriverAgentRunner", "-configuration", "Debug",
            "-destination", "generic/platform=iOS",
            "-derivedDataPath", derived,
            "-allowProvisioningUpdates",
            "ENABLE_DEFAULT_HEADER_SEARCH_PATHS=NO",
            "GCC_TREAT_WARNINGS_AS_ERRORS=NO",
            "OTHER_CFLAGS=$(inherited) -Wno-error=poison-system-directories",
            "RUN_CLANG_STATIC_ANALYZER=NO",
            "build-for-testing",
        ]
        subprocess.run(cmd, check=True, capture_output=True, text=True, timeout=600)
        hits = list(pathlib.Path(derived, "Build", "Products").glob("WebDriverAgentRunner_iphoneos*.xctestrun"))
        if not hits:
            raise RuntimeError("xctestrun not found after build")
        self._xctestrun = str(hits[0])
        return self._xctestrun

    # ---------- 激活单台 ----------
    def activate(self, udid: str, port: int = 8100, reported_udid: str | None = None) -> bool:
        if udid in self.processes and self.processes[udid].poll() is None:
            return True  # 已在运行
        xctestrun = self.ensure_built()
        env = os.environ.copy()
        env["USE_PORT"] = str(port)
        env["EXPANDED_CODE_SIGN_IDENTITY"] = _signing_identity()
        # 让 WDA 上报目标 UDID（与 start-wda.sh 注入一致）
        run = pathlib.Path(xctestrun)
        tmp = run.with_name(run.stem + ".runtime" + run.suffix)
        shutil.copy(run, tmp)
        _set_xctestrun_env(tmp, "WDA_DEVICE_UDID", reported_udid or udid)
        cmd = [
            "xcodebuild", "-xctestrun", str(tmp),
            "-destination", f"id={udid}",
            "test-without-building",
        ]
        log = pathlib.Path("/tmp") / f"wda-{udid[:8]}.log"
        p = subprocess.Popen(cmd, env=env, stdout=log.open("ab"), stderr=subprocess.STDOUT)
        self.processes[udid] = p
        return True

    def stop(self, udid: str) -> bool:
        p = self.processes.pop(udid, None)
        if p is None:
            return False
        p.send_signal(signal.SIGINT)
        try:
            p.wait(timeout=10)
        except subprocess.TimeoutExpired:
            p.kill()
        return True

    # ---------- 健康检查 ----------
    def health(self, ip: str, port: int = 8100, timeout: float = 3) -> dict:
        import httpx
        url = f"http://{ip}:{port}/status"
        try:
            r = httpx.get(url, timeout=timeout)
            data = r.json().get("value", {})
            return {
                "ok": r.status_code == 200,
                "ready": data.get("ready"),
                "ip": data.get("ios", {}).get("ip"),
                "ios_version": (data.get("os") or {}).get("version", ""),
                "status_code": r.status_code,
            }
        except Exception as e:
            return {"ok": False, "error": str(e)}

    def running(self, udid: str) -> bool:
        p = self.processes.get(udid)
        return p is not None and p.poll() is None


# ---------- WhatsApp 发送（移植自平台 internal/wda/whatsapp.go）----------

class WDAError(Exception):
    pass


def _decode_value(data: bytes) -> str:
    env = json.loads(data)
    v = env.get("value")
    if isinstance(v, str):
        return v
    if isinstance(v, dict):
        if v.get("ELEMENT"):
            return v["ELEMENT"]
        if v.get("sessionId"):
            return v["sessionId"]
        if v.get("message"):
            raise WDAError(v["message"])
    return ""


def send_message(ip: str, port: int, phone: str, content: str, timeout: float = 20) -> None:
    """驱动手机 WDA 给指定手机号发送一条文本（同平台 internal/wda 流程）：
    建会话 -> whatsapp://send?phone= 深链 -> 定位输入框 -> 输入 -> 点发送。"""
    import httpx
    base = f"http://{ip}:{port}"
    with httpx.Client(timeout=timeout) as client:
        # 1) 建立 WhatsApp 会话
        r = client.post(f"{base}/session", json={
            "capabilities": {"alwaysMatch": {"bundleId": WHATSAPP_BUNDLE_ID}},
        })
        r.raise_for_status()
        session_id = _decode_value(r.content)
        if not session_id:
            raise WDAError("wda create session returned no sessionId")
        try:
            # 2) 深链打开目标会话
            deeplink = f"whatsapp://send?phone={phone}"
            r = client.post(f"{base}/session/{session_id}/url", json={
                "url": deeplink, "bundleId": WHATSAPP_BUNDLE_ID, "idleTimeoutMs": 3000,
            })
            r.raise_for_status()
            # 3) 等待输入框（最多 15s）
            input_id = _wait_element(client, base, session_id, MESSAGE_INPUT_SELECTOR, 15)
            # 4) 输入内容
            r = client.post(f"{base}/session/{session_id}/element/{input_id}/value",
                            json={"value": [content]})
            r.raise_for_status()
            # 5) 点击发送（候选选择器，最多 10s）
            send_id = _wait_any_element(client, base, session_id, SEND_BUTTON_SELECTORS, 10)
            r = client.post(f"{base}/session/{session_id}/element/{send_id}/click")
            r.raise_for_status()
        finally:
            try:
                client.delete(f"{base}/session/{session_id}")
            except Exception:
                pass


def _find_element(client, base: str, session_id: str, using: str, value: str) -> str:
    r = client.post(f"{base}/session/{session_id}/element", json={"using": using, "value": value})
    if r.status_code >= 300:
        return ""
    return _decode_value(r.content)


def _wait_element(client, base, session_id, selector, timeout: float) -> str:
    using, value = selector
    deadline = time.time() + timeout
    last = ""
    while time.time() < deadline:
        last = _find_element(client, base, session_id, using, value)
        if last:
            return last
        time.sleep(0.5)
    raise WDAError(f"element not found within {timeout}s: {selector}")


def _wait_any_element(client, base, session_id, selectors, timeout: float) -> str:
    deadline = time.time() + timeout
    while time.time() < deadline:
        for using, value in selectors:
            eid = _find_element(client, base, session_id, using, value)
            if eid:
                return eid
        time.sleep(0.5)
    raise WDAError(f"no send button found within {timeout}s: {selectors}")


def _signing_identity() -> str:
    out = subprocess.run(["security", "find-identity", "-v", "-p", "codesigning"],
                         capture_output=True, text=True, timeout=10).stdout
    for line in out.splitlines():
        if "CSSMERR_TP_CERT_REVOKED" in line or "Apple Development" not in line:
            continue
        parts = line.split()
        return parts[1] if len(parts) > 1 else ""
    return ""


def _set_xctestrun_env(xctestrun: pathlib.Path, key: str, value: str) -> None:
    import plistlib
    with xctestrun.open("rb") as f:
        data = plistlib.load(f)
    for bundle in data.values():
        if not isinstance(bundle, dict):
            continue
        env = bundle.setdefault("EnvironmentVariables", {})
        env[key] = value
    with xctestrun.open("wb") as f:
        plistlib.dump(data, f)


manager = WDAManager()
