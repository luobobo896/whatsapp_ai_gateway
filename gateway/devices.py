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

def local_subnets() -> list[str]:
    """本机所有 192.168.x.0/24 网段（手机所在局域网）。"""
    out = subprocess.run(["ifconfig"], capture_output=True, text=True, timeout=5).stdout
    subs = set()
    for line in out.splitlines():
        line = line.strip()
        if line.startswith("inet ") and "192.168." in line:
            ip = line.split()[1]
            subs.add(".".join(ip.split(".")[:3]) + ".0/24")
    return sorted(subs)


def _wda_status_at(ip: str, timeout: float = 0.6):
    """探测某 IP 的 WDA /status；返回 (ready, ios_ip) 或 (None, None)。"""
    try:
        import httpx
        r = httpx.get(f"http://{ip}:8100/status", timeout=timeout)
        value = r.json().get("value", {})
        return bool(value.get("ready")), value.get("ios", {}).get("ip")
    except Exception:
        return None, None


def scan_lan_wda(timeout: float = 0.6, max_workers: int = 64) -> list[dict]:
    """并发扫描本机局域网 8100 端口，返回 WDA 就绪的设备 [{ip, ios_ip}]。"""
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
                    ready, ios_ip = fut.result()
                except Exception:
                    continue
                if ready:
                    results.append({"ip": futs[fut], "ios_ip": ios_ip})
    return results
