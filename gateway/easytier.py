import json, os, pathlib, signal, socket, subprocess, time
from . import config as _config

log = None  # 避免循环导入；用 logging 延迟取

# 集成的 easytier 服务（可选、默认关闭，设计 v6 §5.1/§7.2）：
# 平台下发 easytier:config 后，网关据此启动本机 easytier-core 加入 mesh（后备通道）。
# 查询复用与平台服务端相同的 easytier RPC 门户（easytier-cli 直连，等价平台 internal/easytierrpc）。

BIN_DIR = _config.ROOT / "tools" / "easytier"
CORE_BIN = BIN_DIR / "easytier-core"
CLI_BIN = BIN_DIR / "easytier-cli"
CONFIG_PATH = _config.DATA_DIR / "easytier.json"
TOML_PATH = _config.DATA_DIR / "easytier.toml"
LOG_PATH = pathlib.Path("/tmp") / "easytier-gateway.log"
RPC_PORTAL = "127.0.0.1:15888"

DEFAULTS = {
    "network_name": "wa-ios",
    "network_secret": "",
    "relay_host": "hk.hsddns.com",
    "relay_port": 11010,
    "network_cidr": "10.168.0.0/16",
    "gateway_ipv4": "10.168.1.2",
    "mtu": 1380,
    "tun": False,  # True 需要 root（创建 TUN 绑定虚 IP）；False 无 TUN 纯转发，普通用户可运行
}


def _log():
    import logging
    return logging.getLogger("easytier")


class EasyTierManager:
    def __init__(self):
        self.process: subprocess.Popen | None = None
        self.config = self._load()

    # ---------- 配置 ----------
    def _load(self) -> dict:
        if CONFIG_PATH.exists():
            try:
                data = json.loads(CONFIG_PATH.read_text())
                return {**DEFAULTS, **{k: v for k, v in data.items() if k in DEFAULTS}}
            except Exception:
                pass
        return dict(DEFAULTS)

    def save(self, cfg: dict) -> None:
        self.config = {**self.config, **{k: v for k, v in cfg.items() if k in DEFAULTS}}
        _config.DATA_DIR.mkdir(parents=True, exist_ok=True)
        CONFIG_PATH.write_text(json.dumps(self.config, ensure_ascii=False, indent=2))
        os.chmod(CONFIG_PATH, 0o600)

    def configured(self) -> bool:
        return bool(self.config.get("network_secret")) and bool(self.config.get("relay_host"))

    def public_config(self) -> dict:
        """脱敏配置（secret 只暴露是否已设置），供页面展示/编辑。"""
        c = dict(self.config)
        c["network_secret_set"] = bool(c.get("network_secret"))
        c.pop("network_secret", None)
        c["binary"] = str(CORE_BIN)
        return c

    def apply(self, cfg: dict) -> bool:
        """收到平台 easytier:config：保存并重启集成的 easytier 服务使新配置生效（设计 §7.2）。
        运行中 -> restart；未运行 -> start。"""
        self.save(cfg)
        if not self.configured():
            raise RuntimeError("easytier 配置不完整（缺 network_secret 或 relay_host）")
        if self.running():
            return self.restart()
        return self.start()

    # ---------- 生成配置 / 启停 ----------
    def write_toml(self) -> None:
        c = self.config
        instance = _config.config.cloud.get("gateway_name") or socket.gethostname()
        use_tun = bool(c.get("tun")) and bool(c.get("gateway_ipv4"))
        if use_tun:
            ipv4 = c["gateway_ipv4"]
            prefix = str(c.get("network_cidr", "10.168.0.0/16")).split("/")[-1]
            ipv4_line = f'ipv4 = "{ipv4}/{prefix}"'
            bind_device = "true"
        else:
            ipv4_line = 'ipv4 = ""'  # 空 ipv4：不创建 TUN，普通用户可运行（纯转发节点）
            bind_device = "false"
        toml = f'''instance_name = "{instance}"
hostname = "{instance}"
{ipv4_line}
dhcp = false
rpc_portal = "{RPC_PORTAL}"

[network_identity]
network_name = "{c.get('network_name', 'wa-ios')}"
network_secret = "{c.get('network_secret', '')}"

[flags]
enable_ipv6 = false
use_smoltcp = true
bind_device = {bind_device}
mtu = {int(c.get('mtu', 1380))}

[[peer]]
uri = "tcp://{c.get('relay_host', '')}:{int(c.get('relay_port', 11010))}"
'''
        TOML_PATH.parent.mkdir(parents=True, exist_ok=True)
        TOML_PATH.write_text(toml)
        os.chmod(TOML_PATH, 0o600)

    def running(self) -> bool:
        return self.process is not None and self.process.poll() is None

    def start(self) -> bool:
        if self.running():
            return True
        if not self.configured():
            raise RuntimeError("easytier 配置未就绪（缺 network_secret 或 relay_host）")
        if not CORE_BIN.exists():
            raise RuntimeError(f"easytier-core 不存在：{CORE_BIN}")
        self.write_toml()
        LOG_PATH.parent.mkdir(parents=True, exist_ok=True)
        logf = open(LOG_PATH, "ab")
        self.process = subprocess.Popen(
            [str(CORE_BIN), "--config-file", str(TOML_PATH), "--disable-env-parsing"],
            stdout=logf, stderr=subprocess.STDOUT,
        )
        for _ in range(20):
            if self.process.poll() is not None:
                raise RuntimeError("easytier-core 进程退出（见 /tmp/easytier-gateway.log）")
            if self._cli_ok():
                return True
            time.sleep(1)
        return self._cli_ok()

    def stop(self) -> bool:
        p, self.process = self.process, None
        if p is None:
            return False
        try:
            p.send_signal(signal.SIGINT)
            p.wait(timeout=8)
        except subprocess.TimeoutExpired:
            p.kill()
        return True

    def restart(self) -> bool:
        self.stop()
        time.sleep(1)
        return self.start()

    def recover(self) -> None:
        """网关（本应用）重启后自愈：杀掉遗留的旧 easytier-core 进程并用最新配置重新启动。"""
        if self.configured():
            try:
                subprocess.run(
                    ["pkill", "-f", f"easytier-core --config-file {TOML_PATH}"],
                    capture_output=True, text=True, timeout=5,
                )
            except Exception:
                pass
            time.sleep(1)
            try:
                self.start()
            except Exception as e:
                _log().warning("easytier recover start failed: %s", e)

    # ---------- RPC 查询（easytier-cli 直连 RPC 门户，同平台 easytierrpc 语义）----------
    def _cli(self, args, timeout=5) -> subprocess.CompletedProcess:
        return subprocess.run(
            [str(CLI_BIN), "-p", RPC_PORTAL] + args,
            capture_output=True, text=True, timeout=timeout,
        )

    def _cli_ok(self) -> bool:
        try:
            return self._cli(["node", "info"]).returncode == 0
        except Exception:
            return False

    def node_info(self) -> dict | None:
        try:
            r = self._cli(["-o", "json", "node", "info"])
            if r.returncode != 0:
                return None
            node = json.loads(r.stdout)
            # 脱敏：config 字段含 network_secret 明文，不返回
            node.pop("config", None)
            return node
        except Exception:
            return None

    def peers(self) -> list[dict]:
        try:
            r = self._cli(["-o", "json", "peer", "list"])
            if r.returncode != 0:
                return []
            data = json.loads(r.stdout)
            return data if isinstance(data, list) else []
        except Exception:
            return []

    def status(self) -> dict:
        running = self.running()
        node = self.node_info() if running else None
        peers = self.peers() if running else []
        err = None
        if running and node is None:
            err = "easytier-core 运行中但 RPC 不可达（见 /tmp/easytier-gateway.log）"
        return {
            "configured": self.configured(),
            "running": running,
            "pid": self.process.pid if running and self.process else None,
            "node": node,
            "peers": peers,
            "error": err,
            "log": str(LOG_PATH),
        }


manager = EasyTierManager()
