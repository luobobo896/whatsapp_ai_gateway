import asyncio, logging, time
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


def _config_devices_wda_info(ip: str) -> dict:
    from .devices import wda_info
    try:
        return wda_info(ip, timeout=3)
    except Exception:
        return {}


async def _follow_network_change() -> None:
    """Mac 更换网络后自动跟随：已配置设备 IP 不可达时，重扫当前局域网 WDA，
    用 WDA identifierForVendor(uuid)（在线时已记录）匹配设备；无 uuid 记录时用
    ios_version+model 唯一匹配兜底。命中即更新配置 IP 并上报在线。"""
    from .devices import scan_lan_wda
    cfg = _config.config
    stale = [d for d in cfg.devices
             if d.get("udid") and d.get("ip") and not (d.get("last_health") or {}).get("ok")]
    if not stale:
        return
    found = scan_lan_wda()
    if not found:
        return
    by_uuid = {r["uuid"]: r for r in found if r.get("uuid")}
    for dev in stale:
        old_ip = dev.get("ip")
        hit = by_uuid.get(dev.get("vendor_uuid") or "")
        if not hit:
            # 兜底：按 ios_version（+ 配置了 model 时再对 model）唯一匹配
            cands = []
            for r in found:
                if r.get("ios_version") != dev.get("ios_version"):
                    continue
                if dev.get("model") and (r.get("model") or "").lower() != dev.get("model").lower():
                    continue
                cands.append(r)
            if len(cands) == 1:
                hit = cands[0]
            elif len(cands) > 1:
                log.warning("device %s new IP ambiguous across %d WDA, skip auto-follow", dev["udid"][:8], len(cands))
        if not hit or hit["ip"] == old_ip:
            continue
        dev["ip"] = hit["ip"]
        if hit.get("uuid") and dev.get("vendor_uuid") != hit["uuid"]:
            dev["vendor_uuid"] = hit["uuid"]
        cfg.save()
        log.info("device %s followed network change: %s -> %s", dev["udid"][:8], old_ip, hit["ip"])
        executor.status_q.put_nowait({
            "udid": dev["udid"], "wda_status": "online",
            "error": f"ip updated {old_ip} -> {hit['ip']}",
        })


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
            # 探活时间戳：页面据此判断健康数据是否新鲜（激活后 0~30s 内旧缓存不应显示「异常」）
            h["checked_at"] = time.time()
            # 启动宽限期：WDA 进程刚启动、健康还没就绪时标 starting（页面显示「启动中」而非「异常」）
            h["starting"] = False
            if not h.get("ok") and wda.running(udid):
                secs = wda.started_seconds_ago(udid)
                if secs is not None and secs < WDA_START_GRACE:
                    h["starting"] = True
            dev["last_health"] = h
            if h.get("ios_version"):
                dev["ios_version"] = h["ios_version"]
            # 在线时记录 WDA identifierForVendor(uuid)：Mac 换网/手机 IP 变化后按 uuid 重新匹配手机。
            if h.get("ok") and not dev.get("vendor_uuid"):
                info = _config_devices_wda_info(ip)
                if info.get("uuid"):
                    dev["vendor_uuid"] = info["uuid"]
                    cfg.save()
                    log.info("device %s recorded vendor_uuid=%s", udid[:8], info["uuid"])
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
        # Mac 换网后：把 IP 不可达的已配置设备按 uuid 重新匹配到新 IP
        try:
            await _follow_network_change()
        except Exception as e:
            log.warning("follow network change failed: %s", e)
        try:
            await asyncio.wait_for(stop.wait(), timeout=cfg.health_interval)
        except asyncio.TimeoutError:
            pass
