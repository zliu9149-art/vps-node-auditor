#!/usr/bin/env python3
"""Build private node-provision metadata locally without printing secrets.

中文：仅在 VPS 本地生成 node-provision 私有元数据，不输出任何敏感值。
"""

from __future__ import annotations

import argparse
import base64
import ipaddress
import json
import os
import subprocess
from pathlib import Path
from typing import Any


P25519 = 2**255 - 19
A24 = 121665


def x25519_public(private_text: str) -> str:
    """Derive the X25519 public key without exposing the private key to argv.

    中文：在内存中派生 X25519 公钥，避免将私钥放入子进程命令行。
    """
    padded = private_text + "=" * (-len(private_text) % 4)
    private = bytearray(base64.urlsafe_b64decode(padded))
    if len(private) != 32:
        raise ValueError("Reality private key must decode to 32 bytes")
    private[0] &= 248
    private[31] &= 127
    private[31] |= 64
    scalar = int.from_bytes(private, "little")

    x1, x2, z2, x3, z3, swap = 9, 1, 0, 9, 1, 0
    for bit_index in range(254, -1, -1):
        bit = (scalar >> bit_index) & 1
        swap ^= bit
        if swap:
            x2, x3 = x3, x2
            z2, z3 = z3, z2
        swap = bit
        a = (x2 + z2) % P25519
        aa = a * a % P25519
        b = (x2 - z2) % P25519
        bb = b * b % P25519
        e = (aa - bb) % P25519
        c = (x3 + z3) % P25519
        d = (x3 - z3) % P25519
        da = d * a % P25519
        cb = c * b % P25519
        x3 = (da + cb) ** 2 % P25519
        z3 = x1 * (da - cb) ** 2 % P25519
        x2 = aa * bb % P25519
        z2 = e * (aa + A24 * e) % P25519
    if swap:
        x2, x3 = x3, x2
        z2, z3 = z3, z2
    public = (x2 * pow(z2, P25519 - 2, P25519) % P25519).to_bytes(32, "little")
    return base64.urlsafe_b64encode(public).rstrip(b"=").decode("ascii")


def private_inbound(core: dict[str, Any]) -> dict[str, Any]:
    """Select exactly one enabled VLESS Reality inbound.

    中文：仅允许唯一一个已启用的 VLESS Reality 入站，避免误选生产配置。
    """
    matches = [
        item
        for item in core.get("inbounds", [])
        if item.get("type") == "vless"
        and item.get("tls", {}).get("enabled") is True
        and item.get("tls", {}).get("reality", {}).get("enabled") is True
    ]
    if len(matches) != 1:
        raise ValueError("exactly one enabled VLESS Reality inbound is required")
    return matches[0]


def detect_server(interface_name: str) -> str:
    """Read one global IPv4 address from the configured interface.

    中文：从指定网卡读取一个全局 IPv4 地址，且绝不打印该值。
    """
    output = subprocess.run(
        ["ip", "-json", "address", "show", "dev", interface_name],
        check=True,
        capture_output=True,
        text=True,
    ).stdout
    for link in json.loads(output):
        for address in link.get("addr_info", []):
            value = address.get("local", "")
            if address.get("family") != "inet" or address.get("scope") != "global":
                continue
            parsed = ipaddress.ip_address(value)
            if parsed.version == 4 and parsed.is_global:
                return value
    raise ValueError("no public IPv4 address found on configured interface; use --server")


def first_short_id(value: Any) -> str:
    """Normalize sing-box string/list short_id syntax.

    中文：兼容 sing-box short_id 的字符串或列表形式。
    """
    if isinstance(value, str) and value:
        return value
    if isinstance(value, list):
        for item in value:
            if isinstance(item, str) and item:
                return item
    raise ValueError("Reality short_id is missing")


def build(args: argparse.Namespace) -> dict[str, Any]:
    """Extract only client-safe metadata and fixed local paths.

    中文：仅提取客户端必需元数据与固定本地路径。
    """
    with args.core_config.open("r", encoding="utf-8") as handle:
        core = json.load(handle)
    with args.auditor_config.open("r", encoding="utf-8") as handle:
        auditor = json.load(handle)
    inbound = private_inbound(core)
    tls = inbound["tls"]
    reality = tls["reality"]
    users = inbound.get("users", [])
    flow = next((item.get("flow") for item in users if item.get("flow")), "xtls-rprx-vision")
    server = args.server or detect_server(auditor.get("host", {}).get("interface", "eth0"))
    stats_address = core.get("experimental", {}).get("v2ray_api", {}).get("listen", "127.0.0.1:10085")
    result = {
        "core_config_path": str(args.core_config),
        "core_binary_path": str(args.core_binary),
        "core_unit_path": str(args.core_unit),
        "service_name": "sing-box",
        "inbound_tag": inbound.get("tag", ""),
        "output_directory": str(args.output_directory),
        "backup_directory": str(args.backup_directory),
        "lock_path": auditor.get("collector", {}).get("config_lock_path", "/run/vps-node-auditor/config.lock"),
        "server": server,
        "port": inbound.get("listen_port", 443),
        "reality_public_key": x25519_public(reality.get("private_key", "")),
        "sni": tls.get("server_name") or reality.get("handshake", {}).get("server", ""),
        "short_id": first_short_id(reality.get("short_id")),
        "flow": flow,
        "mixed_port": 7890,
        "stats_address": stats_address,
    }
    required = ("inbound_tag", "server", "reality_public_key", "sni", "short_id", "stats_address")
    if any(not result.get(field) for field in required):
        raise ValueError("one or more required provision fields are missing")
    return result


def write_private(path: Path, data: dict[str, Any]) -> None:
    """Atomically write root-owned mode-0600 JSON.

    中文：以 root 所有、0600 权限原子写入 JSON。
    """
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o750)
    temporary = path.with_name(path.name + ".tmp")
    with temporary.open("w", encoding="utf-8") as handle:
        json.dump(data, handle, ensure_ascii=False, indent=2)
        handle.write("\n")
        handle.flush()
        os.fsync(handle.fileno())
    os.chmod(temporary, 0o600)
    os.chown(temporary, 0, 0)
    os.replace(temporary, path)


def main() -> None:
    """Parse protected paths and create the private configuration.

    中文：解析受保护路径并生成私有配置。
    """
    parser = argparse.ArgumentParser()
    parser.add_argument("--core-config", type=Path, default=Path("/etc/sing-box/config.json"))
    parser.add_argument("--auditor-config", type=Path, default=Path("/etc/vps-node-auditor/config.json"))
    parser.add_argument("--output", type=Path, default=Path("/etc/vps-node-auditor/provision.json"))
    parser.add_argument("--server", default="")
    parser.add_argument("--core-binary", type=Path, default=Path("/usr/local/bin/sing-box"))
    parser.add_argument("--core-unit", type=Path, default=Path("/etc/systemd/system/sing-box.service"))
    parser.add_argument("--output-directory", type=Path, default=Path("/root/vps-node-auditor/clients"))
    parser.add_argument("--backup-directory", type=Path, default=Path("/var/backups/vps-node-auditor/provision"))
    args = parser.parse_args()
    if os.geteuid() != 0:
        raise SystemExit("must run as root")
    write_private(args.output, build(args))
    print("私有 provision 配置已在 VPS 本地生成，未输出任何字段值。")


if __name__ == "__main__":
    main()
