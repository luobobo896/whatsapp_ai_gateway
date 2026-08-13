import asyncio, json, logging, socket, time
from . import config as _config
from .executor import executor

log = logging.getLogger("cloud")

_msg_counter = 0


def _next_msg_id() -> str:
    global _msg_counter
    _msg_counter += 1
    return f"g:{_msg_counter}"


def _sent_at() -> str:
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())


def _envelope(typ: str, payload: dict) -> dict:
    return {"v": 1, "type": typ, "msgId": _next_msg_id(), "sentAt": _sent_at(), "payload": payload}


async def _send(ws, msg: dict) -> None:
    await ws.send(json.dumps(msg, ensure_ascii=False))


async def _pump_queue(ws, q: asyncio.Queue, stop: asyncio.Event, typ: str):
    """把报告队列（item:result / device:status）推给平台。"""
    while not stop.is_set():
        try:
            payload = await asyncio.wait_for(q.get(), timeout=1)
            await _send(ws, _envelope(typ, payload))
        except asyncio.TimeoutError:
            pass
        except Exception as e:
            log.warning("pump %s failed: %s", typ, e)


async def _heartbeat(ws, stop: asyncio.Event, interval: float):
    while not stop.is_set():
        try:
            await asyncio.wait_for(stop.wait(), timeout=interval)
        except asyncio.TimeoutError:
            pass
        if stop.is_set():
            return
        try:
            await _send(ws, _envelope("gateway:heartbeat", {}))
        except Exception:
            return


def _gw_info() -> dict:
    cfg = _config.config
    return {"hostname": socket.gethostname(), "version": "0.2.0"}


def _device_wda_status(udid: str) -> str:
    if executor.is_busy(udid):
        return "busy"
    d = _config.config.device(udid)
    h = (d or {}).get("last_health") or {}
    return "online" if h.get("ok") else "offline"


def _devices_report() -> list[dict]:
    return [
        {
            "udid": d.get("udid"), "name": d.get("name", ""), "model": d.get("model", ""),
            "ios_version": d.get("ios_version", ""),
            "locale": d.get("locale", ""),
            "wda_ip": d.get("ip", ""), "wda_port": int(d.get("port", 8100)),
            "wda_status": _device_wda_status(d.get("udid", "")),
            "whatsapp_version": d.get("whatsapp_version", ""),
        }
        for d in _config.config.devices
    ]


async def run(stop: asyncio.Event, _on_command=None, on_connected=None):
    """网关云通道（三端串联 v6 §5.1）：
    Bearer 网关凭证登录 -> gateway:hello -> device_list -> 心跳 20s；
    收 task:dispatch/task:cancel；上发 item:result/device:status；断线退避重连，重连补报本地结果。
    """
    cfg = _config.config
    url = cfg.cloud.get("ws_url", "")
    token = cfg.cloud.get("token", "")
    name = cfg.cloud.get("gateway_name", "") or socket.gethostname()
    if not url:
        log.info("cloud ws_url 未配置，跳过云连接")
        return
    if not token:
        log.warning("cloud token 未配置，网关将无法通过平台鉴权")
    backoff = 1.0
    while not stop.is_set():
        try:
            import websockets
            headers = {"Authorization": f"Bearer {token}"} if token else {}
            async with websockets.connect(url, additional_headers=headers, ping_interval=None) as ws:
                backoff = 1.0
                log.info("cloud connected: %s (gateway=%s)", url, name)
                if on_connected:
                    on_connected(True)
                await _send(ws, _envelope("gateway:hello", {"name": name, "version": _gw_info()["version"]}))
                # 重连先补报本地结果，再上报设备清单（平台随后补推 pending 任务）。
                executor.resend_persisted()
                await _send(ws, _envelope("device_list", {"devices": _devices_report()}))
                hb = asyncio.create_task(_heartbeat(ws, stop, cfg.heartbeat_interval))
                report = asyncio.create_task(_pump_queue(ws, executor.report_q, stop, "item:result"))
                status = asyncio.create_task(_pump_queue(ws, executor.status_q, stop, "device:status"))
                try:
                    async for raw in ws:
                        try:
                            msg = json.loads(raw)
                        except json.JSONDecodeError:
                            continue
                        typ = msg.get("type")
                        if typ == "task:dispatch":
                            executor.submit(msg.get("payload") or {})
                        elif typ == "task:cancel":
                            executor.cancel((msg.get("payload") or {}).get("task_id", ""))
                        elif typ == "server:disconnect":
                            log.warning("platform asked to disconnect")
                            break
                        elif typ == "easytier:config":
                            # 可选能力：平台下发 easytier 配置 -> 保存并启动集成的 easytier 服务（设计 §5.1/§7.2）
                            try:
                                from .easytier import manager as et
                                et.apply(msg.get("payload") or {})
                                log.info("easytier:config applied, running=%s", et.running())
                            except Exception as e:
                                log.warning("easytier:config apply failed: %s", e)
                        # server:ack 忽略
                finally:
                    hb.cancel()
                    report.cancel()
                    status.cancel()
        except Exception as e:
            log.warning("cloud error: %s", e)
            if on_connected:
                on_connected(False)
        try:
            await asyncio.wait_for(stop.wait(), timeout=backoff)
        except asyncio.TimeoutError:
            pass
        backoff = min(backoff * 2, 30)
