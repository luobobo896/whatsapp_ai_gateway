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
    cfg = _config.config
    known = {d["udid"]: d for d in cfg.devices}
    out = []
    for d in discover():
        udid = d["udid"]
        entry = known.get(udid, {"udid": udid, "ip": "", "port": 8100, "auto_reactivate": True})
        entry = dict(entry)
        entry.update(d)
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
        await asyncio.to_thread(wda.activate, udid, port, udid)
        return {"udid": udid, "status": "activated"}
    if action == "stop":
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


@app.post("/api/devices/{udid}/activate")
async def api_activate(udid: str, port: int = 8100):
    dev = next((d for d in _config.config.devices if d["udid"] == udid), None)
    if dev is None:
        dev = {"udid": udid, "ip": "", "port": port, "auto_reactivate": True}
        _config.config.devices.append(dev)
        _config.config.save()
    await asyncio.to_thread(wda.activate, udid, port, udid)
    return {"udid": udid, "status": "activated"}


@app.post("/api/devices/{udid}/stop")
async def api_stop(udid: str):
    await asyncio.to_thread(wda.stop, udid)
    return {"udid": udid, "status": "stopped"}


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


class EasyTierConfigBody(BaseModel):
    network_name: str | None = None
    network_secret: str | None = None
    relay_host: str | None = None
    relay_port: int | None = None
    network_cidr: str | None = None
    gateway_ipv4: str | None = None
    mtu: int | None = None
    tun: bool | None = None


@app.put("/api/easytier/config")
async def api_easytier_config_put(body: EasyTierConfigBody):
    from .easytier import manager as et
    cfg = {k: v for k, v in body.model_dump().items() if v is not None}
    et.save(cfg)
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
