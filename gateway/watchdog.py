import asyncio, logging
from . import config as _config
from .executor import executor
from .wda import manager as wda

log = logging.getLogger("watchdog")

# WDA 启动宽限期：进程启动后这段时间内健康未就绪视为「启动中」，超过才判定「异常」。
WDA_START_GRACE = 90  # 秒


async def loop(stop: asyncio.Event):
    """后台循环：逐台健康检查，不通且未在激活则自动重新激活；健康状态变化通过 device:status 上报平台。"""
    while not stop.is_set():
        cfg = _config.config
        for dev in list(cfg.devices):
            udid = dev.get("udid")
            ip = dev.get("ip")
            port = int(dev.get("port", 8100))
            if not udid or not ip:
                continue
            try:
                h = wda.health(ip, port, timeout=3)
            except Exception:
                h = {"ok": False}
            prev_ok = (dev.get("last_health") or {}).get("ok")
            # 启动宽限期：WDA 进程刚启动、健康还没就绪时标 starting（页面显示「启动中」而非「异常」）
            h["starting"] = False
            if not h.get("ok") and wda.running(udid):
                secs = wda.started_seconds_ago(udid)
                if secs is not None and secs < WDA_START_GRACE:
                    h["starting"] = True
            dev["last_health"] = h
            if h.get("ios_version"):
                dev["ios_version"] = h["ios_version"]
            new_ok = bool(h.get("ok"))
            if prev_ok is not None and prev_ok != new_ok and not executor.is_busy(udid):
                executor.status_q.put_nowait({
                    "udid": udid,
                    "wda_status": "online" if new_ok else "offline",
                    "error": "" if new_ok else str(h.get("error", "wda down")),
                })
                log.info("device %s wda %s", udid[:8], "online" if new_ok else "offline")
            if not new_ok and dev.get("auto_reactivate", True) and not wda.running(udid):
                log.info("device %s WDA down, reactivating (ip=%s port=%s)", udid[:8], ip, port)
                try:
                    wda.activate(udid, port=port, reported_udid=udid)
                except Exception as e:
                    log.error("reactivate %s failed: %s", udid[:8], e)
        try:
            await asyncio.wait_for(stop.wait(), timeout=cfg.health_interval)
        except asyncio.TimeoutError:
            pass
