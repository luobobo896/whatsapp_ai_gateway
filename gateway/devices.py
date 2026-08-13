import re, subprocess

UDID_RE = re.compile(r"([0-9A-Fa-f]{40})")

def usb_udids() -> list[str]:
    """USB 直连的真机 UDID（ioreg UsbAppleDeviceUDID）。"""
    out = subprocess.run(
        ["ioreg", "-p", "IOUSB", "-l", "-w0"],
        capture_output=True, text=True, timeout=10,
    ).stdout
    seen = []
    for m in re.finditer(r'"UsbAppleDeviceUDID"\s*=\s*"([0-9A-Fa-f]{40})"', out):
        u = m.group(1)
        if u not in seen:
            seen.append(u)
    return seen

def devicectl_devices() -> list[dict]:
    """CoreDevice 可见设备（USB 或 Connect via network），带名称/型号/系统。"""
    out = subprocess.run(
        ["xcrun", "devicectl", "list", "devices"],
        capture_output=True, text=True, timeout=15,
    ).stdout
    devices = []
    for line in out.splitlines():
        parts = line.split()
        if len(parts) < 4:
            continue
        # devicectl 表头: Name Hostname Identifier State Model
        ident = parts[-3]
        state = parts[-2]
        if state != "available" or not UDID_RE.fullmatch(ident):
            continue
        model = parts[-1]
        name = " ".join(parts[:-4])
        devices.append({"udid": ident, "name": name, "model": model})
    return devices

def discover() -> list[dict]:
    """合并 USB + devicectl，返回 [{udid, name, model}]。"""
    seen, result = set(), []
    for d in devicectl_devices():
        seen.add(d["udid"])
        result.append(d)
    for u in usb_udids():
        if u not in seen:
            seen.add(u)
            result.append({"udid": u, "name": "", "model": ""})
    return result

# ---------- 局域网 WDA 探测（自动获取手机 Wi-Fi IP）----------

def _is_private_ipv4(ip: str) -> bool:
    parts = ip.split(".")
    if len(parts) != 4:
        return False
    try:
        a, b = int(parts[0]), int(parts[1])
    except ValueError:
        return False
    if a == 10:
        return True
    if a == 172 and 16 <= b <= 31:
        return True
    if a == 192 and b == 168:
        return True
    return False


def local_subnets() -> list[str]:
    """本机所有私网 IPv4 网段 /24（手机所在局域网，覆盖 10/8、172.16/12、192.168/16，
    兼容 Mac 更换网络后网段变化）。"""
    out = subprocess.run(["ifconfig"], capture_output=True, text=True, timeout=5).stdout
    subs = set()
    for line in out.splitlines():
        line = line.strip()
        if not line.startswith("inet "):
            continue
        ip = line.split()[1]
        if _is_private_ipv4(ip) and not ip.startswith("169.254."):
            subs.add(".".join(ip.split(".")[:3]) + ".0/24")
    return sorted(subs)


def _wda_status_at(ip: str, timeout: float = 0.6):
    """探测某 IP 的 WDA /status；返回 (ready, ios_ip, ios_version) 或 (None, None, None)。"""
    try:
        import httpx
        r = httpx.get(f"http://{ip}:8100/status", timeout=timeout)
        value = r.json().get("value", {})
        return bool(value.get("ready")), value.get("ios", {}).get("ip"), (value.get("os") or {}).get("version", "")
    except Exception:
        return None, None, None


def wda_info(ip: str, timeout: float = 3) -> dict:
    """读取某 WDA 的设备信息（/wda/device/info）：uuid=identifierForVendor（跨网络变化稳定），name/model。"""
    try:
        import httpx
        r = httpx.get(f"http://{ip}:8100/wda/device/info", timeout=timeout)
        v = r.json().get("value", {}) or {}
        return {"uuid": v.get("uuid", ""), "name": v.get("name", ""), "model": v.get("model", "")}
    except Exception:
        return {}


def scan_lan_wda(timeout: float = 0.6, max_workers: int = 64) -> list[dict]:
    """并发扫描本机局域网 8100 端口，返回 WDA 就绪的设备
    [{ip, ios_ip, ios_version, uuid, name, model}]（uuid 用于网络变化后按设备匹配）。"""
    import concurrent.futures
    import ipaddress
    results: list[dict] = []
    for sub in local_subnets():
        try:
            hosts = [str(h) for h in ipaddress.ip_network(sub).hosts()]
        except Exception:
            continue
        with concurrent.futures.ThreadPoolExecutor(max_workers=max_workers) as ex:
            futs = {ex.submit(_wda_status_at, h, timeout): h for h in hosts}
            for fut in concurrent.futures.as_completed(futs):
                try:
                    ready, ios_ip, ios_version = fut.result()
                except Exception:
                    continue
                if ready:
                    ip = futs[fut]
                    info = wda_info(ip, timeout=2)
                    results.append({
                        "ip": ip, "ios_ip": ios_ip, "ios_version": ios_version,
                        "uuid": info.get("uuid", ""), "name": info.get("name", ""),
                        "model": info.get("model", ""),
                    })
    return results
