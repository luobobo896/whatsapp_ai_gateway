import asyncio, logging, pathlib, time
from fastapi import FastAPI, HTTPException

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s: %(message)s")
from fastapi.responses import FileResponse
from fastapi.staticfiles import StaticFiles
from pydantic import BaseModel
from . import config as _config
from . import metrics
from .devices import discover
from .wda import manager as wda
from .executor import executor

STATIC = pathlib.Path(__file__).parent / "static"
app = FastAPI(title="WDA Farm Gateway")

_cloud_task = None
_watchdog_task = None
_stop = asyncio.Event()
_connected = False
_connected_at = None
_last_sent = None


class ReportBody(BaseModel):
    sent_ok: int = 0
    sent_fail: int = 0
    batch_id: str | None = None
    time: float | None = None


def _device_list():
    """设备列表 = 已配置设备(devices.json) ∪ 当前发现到的设备。
    配置过的优先用配置（IP/auto_reactivate）；未配置的显示为「待设置 IP」，
    用户设 IP 后写入配置。USB 直连的设备即使未配 IP 也能被看到并激活（WDA 激活走 USB）。"""
    cfg = _config.config
    configured = {d["udid"]: d for d in cfg.devices}
    seen = set()
    out = []
    for dev in cfg.devices:
        udid = dev.get("udid")
        seen.add(udid)
        entry = dict(dev)
        entry["configured"] = True
        entry["wda_running"] = wda.running(udid)
        entry["metrics"] = metrics.get(udid)
        entry["busy"] = executor.is_busy(udid)
        # 实时修正启动态：进程在跑但健康失败，且进程启动 <90s（宽限期）-> starting
        # （watchdog 探活时机可能错过刚启动的进程，避免页面把「启动中」误判为「异常」）
        h = entry.get("last_health") or {}
        if entry["wda_running"] and not h.get("ok"):
            secs = wda.started_seconds_ago(udid)
            if secs is not None and secs < 90:
                h["starting"] = True
                entry["last_health"] = h
        out.append(entry)
    for d in discover():
        udid = d["udid"]
        if udid in seen:
            continue
        seen.add(udid)
        entry = {"udid": udid, "name": d.get("name", ""), "model": d.get("model", ""),
                 "ip": "", "port": 8100, "auto_reactivate": False, "configured": False}
        entry["wda_running"] = wda.running(udid)
        entry["metrics"] = metrics.get(udid)
        entry["busy"] = executor.is_busy(udid)
        out.append(entry)
    return out


@app.on_event("startup")
async def startup():
    global _cloud_task, _watchdog_task, _connected, _connected_at
    from .cloud import run as cloud_run
    from .watchdog import loop as watchdog_loop
    _watchdog_task = asyncio.create_task(watchdog_loop(_stop))
    # easytier 后备通道自愈：网关重启后按最新配置恢复 easytier 服务（若已配置）。
    from .easytier import manager as et
    et.recover()

    def mark_connected(v):
        global _connected, _connected_at
        _connected = bool(v)
        if v and _connected_at is None:
            _connected_at = time.strftime("%Y-%m-%d %H:%M:%S")

    async def cloud_wrapper():
        global _last_sent
        from .cloud import run as cloud_run
        while not _stop.is_set():
            try:
                await cloud_run(_stop, None, mark_connected)
            except asyncio.CancelledError:
                raise
            except Exception as e:
                _last_sent = f"error: {e}"
            try:
                await asyncio.wait_for(_stop.wait(), timeout=10)
            except asyncio.TimeoutError:
                pass

    _cloud_task = asyncio.create_task(cloud_wrapper())


@app.on_event("shutdown")
async def shutdown():
    _stop.set()
    for t in (_cloud_task, _watchdog_task):
        if t:
            t.cancel()


async def handle_command(cmd: dict):
    """兼容旧协议：云指令 activate/stop/status/report（新协议不再下发 command，保留用于本地调试）。"""
    action = cmd.get("action")
    udid = cmd.get("udid", "")
    port = int(cmd.get("port", 8100))
    dev = next((d for d in _config.config.devices if d["udid"] == udid), None)
    if action == "activate":
        if dev is None:
            raise HTTPException(404, f"device {udid} not configured")
        dev["auto_reactivate"] = True
        _config.config.save()
        await asyncio.to_thread(wda.activate, udid, port, udid)
        return {"udid": udid, "status": "activated"}
    if action == "stop":
        if dev is not None:
            dev["auto_reactivate"] = False
            _config.config.save()
        await asyncio.to_thread(wda.stop, udid)
        return {"udid": udid, "status": "stopped"}
    if action == "status":
        if dev is None:
            raise HTTPException(404, f"device {udid} not configured")
        return await asyncio.to_thread(wda.health, dev["ip"], int(dev.get("port", 8100)))
    if action == "report":
        return await metrics.record(udid, {
            "sent_ok": int(cmd.get("sent_ok", 0)),
            "sent_fail": int(cmd.get("sent_fail", 0)),
            "batch_id": cmd.get("batch_id"),
        })
    raise HTTPException(400, f"unknown action {action}")


@app.get("/")
async def index():
    return FileResponse(STATIC / "index.html")


@app.get("/api/cloud")
async def api_cloud():
    cfg = _config.config
    return {
        "connected": _connected,
        "connected_at": _connected_at,
        "ws_url": cfg.cloud.get("ws_url", ""),
        "gateway_name": cfg.cloud.get("gateway_name", ""),
        "token_configured": bool(cfg.cloud.get("token")),
        "last_error": _last_sent,
        "executor": executor.status(),
    }


@app.get("/api/devices")
async def api_devices():
    return _device_list()


def _wda_status_at(ip: str, port: int = 8100, timeout: float = 3) -> dict:
    """探测某 IP:port 的 WDA /status，返回状态快照（ok/ready/ios_version/error）。"""
    import httpx
    try:
        r = httpx.get(f"http://{ip}:{port}/status", timeout=timeout)
        v = r.json().get("value", {})
        return {"ok": bool(v.get("ready")), "ready": v.get("ready"), "ip": v.get("ios", {}).get("ip", ip),
                "ios_version": (v.get("os") or {}).get("version", ""), "status_code": r.status_code}
    except Exception as e:
        return {"ok": False, "error": str(e)}


async def _wait_wda_ready(udid: str, port: int = 8100, timeout: float = 180):
    """等待 WDA 真正就绪。返回：
    "ready"=健康检查通过；"failed"=进程已退出(激活失败)；"starting"=超时仍在启动。
    未配置 IP 的设备等待 watchdog 自动配 IP 后探活。
    就绪时同步回写该设备 last_health，页面立即显示「在线」（不用等 watchdog 下一轮）。"""
    deadline = time.time() + timeout
    while time.time() < deadline:
        if not wda.running(udid):
            return "failed"
        dev = next((d for d in _config.config.devices if d["udid"] == udid), None)
        ip = (dev or {}).get("ip") or ""
        if ip:
            st = await asyncio.to_thread(_wda_status_at, ip, port)
            if st.get("ok"):
                if dev is not None:
                    dev["last_health"] = {"ok": True, "ready": True, "ip": st.get("ip", ip),
                                          "ios_version": st.get("ios_version", ""), "checked_at": time.time(),
                                          "starting": False}
                return "ready"
        await asyncio.sleep(3)
    return "starting"


@app.post("/api/devices/{udid}/activate")
async def api_activate(udid: str, port: int = 8100):
    # WDA 激活走 USB 直连（xcodebuild -destination id=udid），不依赖局域网 IP；
    # 未配置设备允许激活，但不写入 devices.json（设 IP 时才会持久化）。
    from .devices import discover
    known = {d["udid"] for d in _config.config.devices} | {d["udid"] for d in discover()}
    if udid not in known:
        raise HTTPException(404, "设备未发现（请确认手机已 USB 连接并信任此电脑）")
    dev = next((d for d in _config.config.devices if d["udid"] == udid), None)
    if dev is None:
        dev = {"udid": udid, "ip": "", "port": port, "auto_reactivate": True}
    else:
        dev["auto_reactivate"] = True  # 激活 = 恢复看护（watchdog 自动重拉）
        _config.config.save()
    try:
        await asyncio.to_thread(wda.activate, udid, port, udid)
    except Exception as e:
        raise HTTPException(400, f"激活失败：{e}")
    # 等待 WDA 真正就绪才返回成功：前端点击立即显示「启动中」，只有健康检查通过才算 activated
    result = await _wait_wda_ready(udid, port)
    if result == "failed":
        raise HTTPException(400, "激活失败：WDA 进程已退出（请确认手机解锁并信任此电脑）")
    if result == "starting":
        return {"udid": udid, "status": "starting", "ready": False, "auto_reactivate": True,
                "message": "WDA 仍在启动（尚未就绪）"}
    return {"udid": udid, "status": "activated", "ready": True, "auto_reactivate": True}


@app.post("/api/devices/{udid}/stop")
async def api_stop(udid: str):
    # 停止 = 真正停止：关掉自动看护，watchdog 不再自动拉起
    dev = next((d for d in _config.config.devices if d["udid"] == udid), None)
    if dev is not None:
        dev["auto_reactivate"] = False
        _config.config.save()
    await asyncio.to_thread(wda.stop, udid)
    return {"udid": udid, "status": "stopped", "auto_reactivate": False}


@app.get("/api/devices/{udid}/health")
async def api_health(udid: str):
    dev = next((d for d in _config.config.devices if d["udid"] == udid), None)
    if dev is None or not dev.get("ip"):
        raise HTTPException(404, f"device {udid} has no ip configured")
    return await asyncio.to_thread(wda.health, dev["ip"], int(dev.get("port", 8100)))


@app.post("/api/devices/{udid}/set-ip")
async def api_set_ip(udid: str, ip: str, port: int = 8100):
    dev = next((d for d in _config.config.devices if d["udid"] == udid), None)
    if dev is None:
        dev = {"udid": udid, "auto_reactivate": True}
        _config.config.devices.append(dev)
    dev["ip"], dev["port"] = ip, port
    _config.config.save()
    return {"udid": udid, "ip": ip, "port": port}


@app.post("/api/devices/{udid}/report")
async def api_report(udid: str, body: ReportBody):
    """发送结果上报入口：本地/调试结果上报（兼容旧协议；新协议由网关执行器直接发 item:result）。"""
    return await metrics.record(udid, body.model_dump())


@app.get("/api/devices/{udid}/metrics")
async def api_metrics(udid: str):
    return metrics.get(udid)


app.mount("/static", StaticFiles(directory=str(STATIC)), name="static")


# ---------- easytier（可选，默认关闭；配置由平台 easytier:config 下发或本地 PUT 维护）----------

@app.get("/api/easytier/status")
async def api_easytier_status():
    from .easytier import manager as et
    return et.status()


@app.get("/api/easytier/config")
async def api_easytier_config():
    from .easytier import manager as et
    return et.public_config()


class EasyTierActionBody(BaseModel):
    action: str


@app.post("/api/easytier/action")
async def api_easytier_action(body: EasyTierActionBody):
    from .easytier import manager as et
    action = body.action
    if action == "start":
        try:
            ok = et.start()
        except Exception as e:
            raise HTTPException(400, f"启动失败：{e}")
        return {"ok": ok, "running": et.running()}
    if action == "stop":
        return {"ok": et.stop(), "running": et.running()}
    if action == "restart":
        try:
            ok = et.restart()
        except Exception as e:
            raise HTTPException(400, f"重启失败：{e}")
        return {"ok": ok, "running": et.running()}
    raise HTTPException(400, "action 必须为 start/stop/restart")
