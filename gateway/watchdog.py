import asyncio, logging
from . import config as _config
from .executor import executor
from .wda import manager as wda

log = logging.getLogger("watchdog")

# WDA 启动宽限期：进程启动后这段时间内健康未就绪视为「启动中」，超过才判定「异常」。
WDA_START_GRACE = 90  # 秒


async def _auto_assign_ip() -> None:
    """对「已激活但未配置 IP」的设备自动探测局域网 WDA 并写入配置（无需手动设 IP）。
    候选 = 扫描到的 WDA IP - 已配置设备的 IP；恰好一个时自动分配，多台时保持待设置。"""
    from .devices import discover, scan_lan_wda
    cfg = _config.config
    configured = {d["udid"]: d for d in cfg.devices}
    known_ips = {d.get("ip") for d in cfg.devices if d.get("ip")}
    pending = [d for d in discover()
               if d["udid"] not in configured and wda.running(d["udid"])]
    if not pending:
        return
    found = scan_lan_wda()
    candidates = [r for r in found if r["ip"] not in known_ips and r["ip"] != "127.0.0.1"]
    if len(pending) == 1 and len(candidates) == 1:
        udid = pending[0]["udid"]
        ip = candidates[0]["ip"]
        cfg.devices.append({"udid": udid, "ip": ip, "port": 8100, "auto_reactivate": True})
        cfg.save()
        log.info("auto-assigned WDA IP %s to device %s", ip, udid[:8])


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
        # 已激活但未配置 IP 的设备：自动探测局域网 IP（无需手动设 IP）
        try:
            await _auto_assign_ip()
        except Exception as e:
            log.warning("auto assign ip failed: %s", e)
        try:
            await asyncio.wait_for(stop.wait(), timeout=cfg.health_interval)
        except asyncio.TimeoutError:
            pass
