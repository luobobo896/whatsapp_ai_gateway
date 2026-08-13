import json, os, pathlib

ROOT = pathlib.Path(__file__).resolve().parent.parent  # gateway/
DEFAULT_CONFIG = ROOT / "devices.json"
DATA_DIR = ROOT / "data"

class Config:
    def __init__(self, path: pathlib.Path = DEFAULT_CONFIG):
        self.path = path
        data = {}
        if path.exists():
            data = json.loads(path.read_text())
        self.cloud = data.get("cloud", {}) or {}
        # cloud: {ws_url, token(网关凭证), gateway_name, enabled, heartbeat_interval}
        self.devices = data.get("devices", []) or []   # [{udid, ip, port, auto_reactivate}]
        self.health_interval = float(os.environ.get("GATEWAY_HEALTH_INTERVAL", data.get("health_interval", 30)))
        self.heartbeat_interval = float(self.cloud.get("heartbeat_interval", 20))
        # WDA 工程路径（构建 WebDriverAgent 用）：默认同级 WhatsAppDeviceAgent，可用 WDA_PROJECT_ROOT 覆盖
        self.project_root = pathlib.Path(os.environ.get("WDA_PROJECT_ROOT", ROOT.parent / "whatsapp_ai_ios" / "WhatsAppDeviceAgent"))
        self.derived_data = data.get("derived_data", "/tmp/WebDriverAgentFarmDerived")

    def device(self, udid: str) -> dict | None:
        return next((d for d in self.devices if d.get("udid") == udid), None)

    def save(self):
        self.path.write_text(json.dumps({
            "cloud": self.cloud,
            "devices": self.devices,
            "health_interval": self.health_interval,
        }, ensure_ascii=False, indent=2))

config = Config()
