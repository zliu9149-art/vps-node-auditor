#!/usr/bin/env python3
"""Apply the documented production defaults without printing secret fields.

中文：应用文档规定的生产默认值，且不输出任何秘密字段。
"""

from __future__ import annotations

import argparse
import grp
import json
import os
from pathlib import Path


def update(path: Path) -> None:
    """Atomically update only auditor-owned non-secret settings.

    中文：仅原子更新审计器拥有的非敏感设置。
    """
    with path.open("r", encoding="utf-8") as handle:
        data = json.load(handle)

    data.setdefault("collector", {})["config_lock_path"] = "/run/vps-node-auditor/config.lock"
    data["host"] = {"disabled": False, "interface": "eth0", "command_timeout": "5s"}
    data["provider"] = {
        "monthly_quota_bytes": 1_000_000_000_000,
        "cycle_start_day": 9,
        "billing_timezone": "Asia/Hong_Kong",
        "traffic_direction": "both",
    }
    data["retention"] = {
        "minute_days": 90,
        "event_days": 14,
        "rollup_days": 400,
        "backup_count": 14,
    }
    data["alerts"] = {
        "quota_percentages": [50, 75, 90, 95],
        "traffic_difference_percent": 10,
        "consecutive_failures": 3,
        "user_hourly_bytes": 0,
        "user_daily_bytes": 0,
        "historical_spike_factor": 0,
    }

    temporary = path.with_name(path.name + ".mvp.tmp")
    with temporary.open("w", encoding="utf-8") as handle:
        json.dump(data, handle, ensure_ascii=False, indent=2)
        handle.write("\n")
        handle.flush()
        os.fsync(handle.fileno())
    os.chmod(temporary, 0o640)
    os.chown(temporary, 0, grp.getgrnam("node-audit").gr_gid)
    os.replace(temporary, path)


def main() -> None:
    """Parse the explicit configuration path and apply the migration.

    中文：解析显式配置路径并应用迁移。
    """
    parser = argparse.ArgumentParser()
    parser.add_argument("path", type=Path)
    args = parser.parse_args()
    update(args.path)
    print("审计器生产默认配置已更新。")


if __name__ == "__main__":
    main()
