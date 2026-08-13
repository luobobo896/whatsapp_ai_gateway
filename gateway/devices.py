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
