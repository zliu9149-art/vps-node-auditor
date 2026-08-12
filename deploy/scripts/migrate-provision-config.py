#!/usr/bin/env python3
"""Atomically remove obsolete pre-v1 fields without printing secret values."""

import json
import os
import pathlib
import stat
import sys
import tempfile


def migrate(path: pathlib.Path) -> None:
    metadata = path.stat()
    with path.open("r", encoding="utf-8") as stream:
        payload = json.load(stream)
    if not isinstance(payload, dict):
        raise ValueError("provision config root must be an object")
    payload.pop("reload_verified", None)
    descriptor, temporary_name = tempfile.mkstemp(prefix=".provision-migrate-", dir=path.parent)
    temporary = pathlib.Path(temporary_name)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8", newline="\n") as stream:
            json.dump(payload, stream, indent=2, ensure_ascii=False)
            stream.write("\n")
            stream.flush()
            os.fsync(stream.fileno())
        os.chmod(temporary, stat.S_IMODE(metadata.st_mode))
        os.chown(temporary, metadata.st_uid, metadata.st_gid)
        os.replace(temporary, path)
    finally:
        temporary.unlink(missing_ok=True)


if __name__ == "__main__":
    if len(sys.argv) != 2:
        raise SystemExit("usage: migrate-provision-config.py PATH")
    migrate(pathlib.Path(sys.argv[1]))
