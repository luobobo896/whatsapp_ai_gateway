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
        self.started_at: dict[str, float] = {}             # udid -> 进程启动时间（区分「启动中」与「异常」）
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
        self.started_at[udid] = time.time()
        return True

    def stop(self, udid: str) -> bool:
        p = self.processes.pop(udid, None)
        self.started_at.pop(udid, None)
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

    def started_seconds_ago(self, udid: str) -> float | None:
        """进程已启动秒数（无进程返回 None），用于区分「启动中」与「异常」。"""
        t = self.started_at.get(udid)
        if t is None or not self.running(udid):
            return None
        return time.time() - t


# ---------- WhatsApp 发送（移植自平台 internal/wda/whatsapp.go；iOS 15.8 深链不可用时走 UI 兜底）----------

class WDAError(Exception):
    pass


# WhatsApp 候选 bundle id（普通 WhatsApp / WhatsApp Business），按序自动识别。
WHATSAPP_BUNDLE_IDS = ["net.whatsapp.WhatsApp", "net.whatsapp.WhatsAppSMB"]
# WhatsApp 界面元素选择器（真机联调时按需覆盖）。
MESSAGE_INPUT_SELECTOR = ("class chain", "**/XCUIElementTypeTextView[1]")
SEND_BUTTON_SELECTORS = [
    ("accessibility id", "ChatBar_SendButton"),
    ("accessibility id", "Send"),
    ("accessibility id", "send"),
    ("predicate string", "name == 'Send'"),
    ("predicate string", "name == '发送'"),
    ("predicate string", "label == '发送'"),
]
# 从聊天页返回聊天列表的返回键候选（label 带 RTL 不可见字符，须用 CONTAINS；限定 Button 避免误匹配标题）。
BACK_TO_CHATS_SELECTORS = [
    ("predicate string", "type == 'XCUIElementTypeButton' AND label CONTAINS '聊天'"),
    ("predicate string", "type == 'XCUIElementTypeButton' AND label CONTAINS 'Chats'"),
    ("predicate string", "type == 'XCUIElementTypeButton' AND name CONTAINS 'Back'"),
    ("predicate string", "type == 'XCUIElementTypeButton' AND name CONTAINS '返回'"),
]


def _digits(s: str) -> str:
    return "".join(ch for ch in (s or "") if ch.isdigit())


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


def _create_session(client, base: str, bundle_id: str | None) -> tuple[str, str]:
    """建立 WhatsApp 会话，返回 (session_id, 实际 bundle id)。
    未指定 bundle_id 时按候选列表自动识别（普通 WhatsApp / WhatsApp Business）。

    shouldTerminateApp=false：会话删除/重建时 WDA 不再强杀 WhatsApp（否则每条发完 App 闪退）；
    forceAppLaunch=false：App 已在前台时复用，不终止重拉（批量发送间不重启，也避免闪退观感）。"""
    candidates = [bundle_id] if bundle_id else WHATSAPP_BUNDLE_IDS
    for bid in candidates:
        r = client.post(f"{base}/session", json={
            "capabilities": {"alwaysMatch": {
                "bundleId": bid,
                "shouldTerminateApp": False,
                "forceAppLaunch": False,
            }},
        })
        if r.status_code >= 300:
            continue
        sid = _decode_value(r.content)
        if sid:
            return sid, bid
    raise WDAError(f"whatsapp not installed (tried: {', '.join(candidates)})")


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


def _source_cells(client, base: str, session_id: str) -> list:
    """返回当前页面按文档顺序排列的 XCUIElementTypeCell 列表（解析 accessibility 树）。"""
    import xml.etree.ElementTree as ET
    r = client.get(f"{base}/session/{session_id}/source")
    r.raise_for_status()
    xml = _decode_value(r.content)
    if not xml:
        return []
    root = ET.fromstring(xml)
    return [el for el in root.iter() if el.tag.endswith("XCUIElementTypeCell")]


def _cell_digits(cell) -> str:
    return _digits(cell.get("name") or "")


def _first_chat_index(client, base: str, session_id: str) -> int | None:
    """聊天列表中第一个真实会话 cell 的序号（1 起）；跳过筛选/工具行。"""
    cells = _source_cells(client, base, session_id)
    for i, cell in enumerate(cells, start=1):
        if _cell_digits(cell):
            return i
        for sub in cell.iter():
            if (sub.get("name") or "") == "WAChatSessionCell_Message":
                return i
    return None


def _chat_index_by_phone(client, base: str, session_id: str, digits: str) -> int | None:
    """按号码在聊天列表中找会话 cell（name 去非数字后 == 目标号码），返回序号；找不到返回 None。"""
    cells = _source_cells(client, base, session_id)
    for i, cell in enumerate(cells, start=1):
        if _cell_digits(cell) == digits:
            return i
    return None


def _tap_cell(client, base: str, session_id: str, idx: int) -> None:
    eid = _find_element(client, base, session_id, "class chain", f"**/XCUIElementTypeCell[{idx}]")
    if not eid:
        raise WDAError(f"chat cell [{idx}] not found")
    r = client.post(f"{base}/session/{session_id}/element/{eid}/click")
    r.raise_for_status()
    time.sleep(1.5)


def _goto_chat_list(client, base: str, session_id: str) -> None:
    """确保停在聊天列表页：当前若在聊天页（输入框可见）则点返回。"""
    if not _find_element(client, base, session_id, *MESSAGE_INPUT_SELECTOR):
        return
    for using, value in BACK_TO_CHATS_SELECTORS:
        eid = _find_element(client, base, session_id, using, value)
        if eid:
            r = client.post(f"{base}/session/{session_id}/element/{eid}/click")
            r.raise_for_status()
            time.sleep(1.5)
            return


def _ios_major(client, base: str) -> int:
    """读取 WDA /status 的 iOS 主版本；读取失败返回 0（保守按支持深链处理）。"""
    try:
        r = client.get(f"{base}/status")
        v = (r.json().get("value") or {}).get("os", {}).get("version", "")
        return int(str(v).split(".")[0] or 0)
    except Exception:
        return 0


def _open_target_chat(client, base: str, session_id: str, bundle_id: str, phone: str, ios_major: int = 0) -> None:
    """打开指定号码的会话：iOS 16.4+ 走深链；iOS 15.8 深链不可用，直接回退到聊天列表按号码匹配/新聊天搜索。"""
    digits = _digits(phone)
    if not digits:
        raise WDAError(f"invalid phone: {phone!r}")
    if ios_major >= 16:
        r = client.post(f"{base}/session/{session_id}/url", json={
            "url": f"whatsapp://send?phone={digits}", "bundleId": bundle_id, "idleTimeoutMs": 3000,
        })
        if r.status_code < 300:
            return  # 深链成功打开目标会话
    _goto_chat_list(client, base, session_id)
    idx = _chat_index_by_phone(client, base, session_id, digits)
    if idx is not None:
        _tap_cell(client, base, session_id, idx)
        return
    # 聊天列表没有该号码：走「新聊天→搜索→选中联系人」兜底（号码是联系人/可搜到时可用）。
    if _open_new_chat_by_phone(client, base, session_id, digits):
        return
    raise WDAError(
        f"deep link unsupported and no chat/contact for {digits} "
        f"(iOS < 16.4 无法通过深链打开陌生号码会话)"
    )


def _open_new_chat_by_phone(client, base: str, session_id: str, digits: str) -> bool:
    """新聊天 -> 搜索号码 -> 命中联系人则点其「聊天」动作并进入会话；返回是否成功。"""
    newchat = _find_element(client, base, session_id, "accessibility id", "NavigationBar_NewChatButton")
    if not newchat:
        return False
    r = client.post(f"{base}/session/{session_id}/element/{newchat}/click")
    r.raise_for_status()
    time.sleep(1.5)
    sf = _find_element(client, base, session_id, "class chain", "**/XCUIElementTypeSearchField")
    if not sf:
        return False
    r = client.post(f"{base}/session/{session_id}/element/{sf}/click")
    r.raise_for_status()
    time.sleep(0.8)
    r = client.post(f"{base}/session/{session_id}/element/{sf}/value", json={"value": [digits]})
    r.raise_for_status()
    time.sleep(2.5)
    # 联系人 cell（name == 'PickerView_ContactCell'）：动作在 cell 右侧（「聊天」/「给自己发消息」），
    # cell 中央是姓名点不到动作区。依次尝试：cell 右侧坐标点击 -> cell 内动作文本点击。
    cell = _find_element(client, base, session_id, "accessibility id", "PickerView_ContactCell")
    if not cell:
        return False
    try:
        rect = client.get(f"{base}/session/{session_id}/element/{cell}/rect").json()["value"]
        x = int(rect["x"] + rect["width"] - 30)
        y = int(rect["y"] + rect["height"] / 2)
        r = client.post(f"{base}/session/{session_id}/wda/tap", json={"x": x, "y": y})
        r.raise_for_status()
        if _chat_opened(client, base, session_id, 6):
            return True
    except Exception:
        pass
    # 兜底：点 cell 内动作文本（自己号码=PickerView_ContactCell_PhoneNumber；联系人=聊天）
    for name in ("PickerView_ContactCell_PhoneNumber", "聊天"):
        sel = ("class chain",
               f"**/XCUIElementTypeCell[`name == 'PickerView_ContactCell'`]/**/XCUIElementTypeStaticText[`name == '{name}'`]")
        act = _find_element(client, base, session_id, *sel)
        if not act:
            continue
        try:
            r = client.post(f"{base}/session/{session_id}/element/{act}/click")
            r.raise_for_status()
            if _chat_opened(client, base, session_id, 6):
                return True
        except Exception:
            continue
    return False


def _chat_opened(client, base: str, session_id: str, timeout: float) -> bool:
    """点击联系人动作后是否已进入会话（输入框可见）。"""
    deadline = time.time() + timeout
    while time.time() < deadline:
        if _find_element(client, base, session_id, *MESSAGE_INPUT_SELECTOR):
            return True
        time.sleep(0.5)
    return False


def _open_default_chat(client, base: str, session_id: str) -> None:
    """不指定号码：当前已有打开的会话则直接使用，否则打开聊天列表第一个会话。"""
    if _find_element(client, base, session_id, *MESSAGE_INPUT_SELECTOR):
        return
    idx = _first_chat_index(client, base, session_id)
    if idx is None:
        raise WDAError("no chat available to send to")
    _tap_cell(client, base, session_id, idx)


def send_message(ip: str, port: int, phone: str, content: str, bundle_id: str | None = None, timeout: float = 20) -> None:
    """驱动手机 WDA 发送一条文本：
    - 传了手机号：深链（iOS 16.4+）或聊天列表按号码打开会话；
    - 没传手机号：发送到当前已打开的会话，否则聊天列表第一个会话。
    之后定位输入框 -> 输入 -> 点发送。"""
    import httpx
    base = f"http://{ip}:{port}"
    with httpx.Client(timeout=timeout) as client:
        session_id, bid = _create_session(client, base, bundle_id)
        try:
            if phone:
                _open_target_chat(client, base, session_id, bid, phone, _ios_major(client, base))
            else:
                _open_default_chat(client, base, session_id)
            input_id = _wait_element(client, base, session_id, MESSAGE_INPUT_SELECTOR, 15)
            r = client.post(f"{base}/session/{session_id}/element/{input_id}/value",
                            json={"value": [content]})
            r.raise_for_status()
            send_id = _wait_any_element(client, base, session_id, SEND_BUTTON_SELECTORS, 10)
            r = client.post(f"{base}/session/{session_id}/element/{send_id}/click")
            r.raise_for_status()
        finally:
            try:
                client.delete(f"{base}/session/{session_id}")
            except Exception:
                pass


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
