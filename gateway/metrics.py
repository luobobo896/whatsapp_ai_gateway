import asyncio, time

"""发送结果核心指标：由网关收集（POST /report 或云指令），再统一上报平台。"""

_lock = asyncio.Lock()
_store: dict[str, dict] = {}
report_queue: asyncio.Queue = asyncio.Queue()   # (udid, metrics) 待推送平台

def _empty() -> dict:
    return {"sent_ok": 0, "sent_fail": 0, "total": 0, "batch_id": None, "last_time": None}

async def record(udid: str, data: dict) -> dict:
    """累加一次发送结果。data: {sent_ok, sent_fail, batch_id?, time?}"""
    async with _lock:
        m = _store.setdefault(udid, _empty())
        m["sent_ok"] += int(data.get("sent_ok", 0) or 0)
        m["sent_fail"] += int(data.get("sent_fail", 0) or 0)
        m["total"] = m["sent_ok"] + m["sent_fail"]
        if data.get("batch_id"):
            m["batch_id"] = data["batch_id"]
        m["last_time"] = data.get("time") or time.time()
        snapshot = dict(m)
    await report_queue.put((udid, snapshot))
    return snapshot

def get(udid: str) -> dict:
    return dict(_store.get(udid, _empty()))

def all_metrics() -> dict:
    return {u: dict(m) for u, m in _store.items()}
