import asyncio, logging
from . import config as _config
from .executor import executor
from .wda import manager as wda

log = logging.getLogger("watchdog")


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
