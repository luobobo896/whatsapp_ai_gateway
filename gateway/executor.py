import asyncio, json, logging, time
from pathlib import Path
from . import config as _config
from . import metrics
from .wda import send_message

log = logging.getLogger("executor")

# 本地持久化目录：每条 item 先写结果文件再上报（at-least-once，§9-13）。
RESULTS_DIR = _config.DATA_DIR / "results"


def _result_file(task_id: str) -> Path:
    return RESULTS_DIR / f"{task_id}.json"


def _load_results(task_id: str) -> dict:
    p = _result_file(task_id)
    if not p.exists():
        return {}
    try:
        return json.loads(p.read_text())
    except Exception:
        return {}


def _persist_result(task_id: str, item_id: str, phone: str, status: str, error: str, duration_ms: int) -> None:
    RESULTS_DIR.mkdir(parents=True, exist_ok=True)
    data = _load_results(task_id)
    data[item_id] = {"phone": phone, "status": status, "error": error, "duration_ms": duration_ms}
    tmp = _result_file(task_id).with_suffix(".json.tmp")
    tmp.write_text(json.dumps(data, ensure_ascii=False))
    tmp.replace(_result_file(task_id))


class Executor:
    """task:dispatch / task:cancel -> 发送循环（per-UDID 串行）-> 本地持久化 -> item:result。

    - 同一 UDID 的任务串行执行（每 UDID 一个 worker）。
    - 每条 item 先本地持久化再上报；收到 task:cancel 停止循环并把未执行 items 标 cancelled 上报。
    - 重连补报：resend_persisted() 把本地已持久化结果重新入队上报（平台按 item_id 幂等）。
    """

    def __init__(self):
        self._queues: dict[str, asyncio.Queue] = {}      # udid -> queue of dispatch payloads
        self._workers: dict[str, asyncio.Task] = {}      # udid -> worker task
        self._cancel: dict[str, asyncio.Event] = {}      # task_id -> cancel event
        self._busy: set[str] = set()                     # udids currently executing
        self.report_q: asyncio.Queue = asyncio.Queue()   # item:result dicts 待上报
        self.status_q: asyncio.Queue = asyncio.Queue()   # device:status dicts 待上报

    # ---------- 对外入口 ----------
    def submit(self, payload: dict) -> None:
        """收到 task:dispatch：入队（同一 task 重复下发幂等）。"""
        task_id = payload.get("task_id", "")
        udid = payload.get("udid", "")
        if not task_id or not udid:
            log.warning("dispatch missing task_id/udid: %s", payload)
            return
        if task_id in self._cancel:
            log.info("dispatch already known, skip: %s", task_id)
            return
        self._cancel[task_id] = asyncio.Event()
        q = self._queues.setdefault(udid, asyncio.Queue())
        q.put_nowait(dict(payload))
        if udid not in self._workers or self._workers[udid].done():
            self._workers[udid] = asyncio.create_task(self._run_udid(udid))

    def cancel(self, task_id: str) -> None:
        ev = self._cancel.get(task_id)
        if ev:
            ev.set()
            log.info("task cancel requested: %s", task_id)

    def is_busy(self, udid: str) -> bool:
        return udid in self._busy

    def status(self) -> dict:
        return {
            "busy_udids": sorted(self._busy),
            "queued": {u: q.qsize() for u, q in self._queues.items()},
            "pending_tasks": sorted(self._cancel.keys()),
        }

    def resend_persisted(self) -> None:
        """重连后补报本地已持久化但可能未上报的结果（平台按 item_id 幂等忽略重复）。"""
        if not RESULTS_DIR.exists():
            return
        count = 0
        for p in sorted(RESULTS_DIR.glob("*.json")):
            try:
                data = json.loads(p.read_text())
            except Exception:
                continue
            for item_id, r in data.items():
                self.report_q.put_nowait({
                    "task_id": p.stem, "item_id": item_id, "phone": r.get("phone", ""),
                    "status": r.get("status", "failed"), "error": r.get("error", ""),
                    "duration_ms": int(r.get("duration_ms", 0)),
                })
                count += 1
        if count:
            log.info("resend %d persisted results", count)

    # ---------- worker ----------
    async def _run_udid(self, udid: str) -> None:
        while True:
            q = self._queues.get(udid)
            if q is None or q.empty():
                # 队列空了，worker 退出；下一次 submit 会重新创建。
                self._workers.pop(udid, None)
                return
            payload = await q.get()
            try:
                await self._process_task(udid, payload)
            except Exception as e:
                log.exception("process task failed: %s", e)
            finally:
                self._busy.discard(udid)

    async def _process_task(self, udid: str, payload: dict) -> None:
        task_id = payload["task_id"]
        cancel_ev = self._cancel.get(task_id)
        if cancel_ev is None:
            return
        dev = _config.config.device(udid)
        ip = (dev or {}).get("ip", "")
        port = int((dev or {}).get("port", 8100))
        items = payload.get("items", []) or []
        interval = int(payload.get("interval_sec", 0) or 0)
        self._busy.add(udid)
        await self._status(udid, "busy", "")
        try:
            for item in items:
                item_id = item.get("item_id", "")
                if cancel_ev.is_set():
                    await self._report(item_id, item, task_id, "cancelled", "cancelled by platform", 0)
                    continue
                if _load_results(task_id).get(item_id):
                    continue  # 本地已记账（重复下发/重连续发），跳过避免重复发送
                phone = item.get("phone", "")
                if not ip:
                    await self._report(item_id, item, task_id, "failed", "device ip not configured", 0)
                    continue
                t0 = time.time()
                status, err = "sent", ""
                try:
                    await asyncio.to_thread(send_message, ip, port, phone, payload.get("content", ""), (dev or {}).get("whatsapp_bundle_id"))
                except Exception as e:
                    status, err = "failed", str(e)[:500]
                duration_ms = int((time.time() - t0) * 1000)
                _persist_result(task_id, item_id, phone, status, err, duration_ms)
                await self._report(item_id, item, task_id, status, err, duration_ms)
                # 网关本地发送统计（/api/devices 与 Web 页展示；sent/failed 计入，cancelled 不计）
                if status in ("sent", "failed"):
                    await metrics.record(udid, {
                        "sent_ok": 1 if status == "sent" else 0,
                        "sent_fail": 1 if status == "failed" else 0,
                        "batch_id": task_id,
                    })
                log.info("item done task=%s item=%s phone=%s status=%s", task_id, item_id, phone, status)
                if status == "failed" and ("not reachable" in err.lower() or "connection" in err.lower()):
                    log.warning("device unreachable, stop current task: %s", task_id)
                    break
                if interval > 0:
                    try:
                        await asyncio.wait_for(cancel_ev.wait(), timeout=interval)
                    except asyncio.TimeoutError:
                        pass
        finally:
            self._busy.discard(udid)
            self._cancel.pop(task_id, None)
            await self._status(udid, "online", "")

    async def _report(self, item_id: str, item: dict, task_id: str, status: str, error: str, duration_ms: int) -> None:
        self.report_q.put_nowait({
            "task_id": task_id, "item_id": item_id, "phone": item.get("phone", ""),
            "status": status, "error": error, "duration_ms": duration_ms,
        })

    async def _status(self, udid: str, wda_status: str, error: str) -> None:
        self.status_q.put_nowait({"udid": udid, "wda_status": wda_status, "error": error})


executor = Executor()
