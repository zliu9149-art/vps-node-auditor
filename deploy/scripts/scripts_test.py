#!/usr/bin/env python3
"""Static contract tests for privileged deployment scripts."""

from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parents[2]


class ScriptContracts(unittest.TestCase):
    def read(self, name: str) -> str:
        return (ROOT / "deploy" / "scripts" / name).read_text(encoding="utf-8")

    def test_install_has_lock_unique_rollback_and_failure_trap(self) -> None:
        script = self.read("install.sh")
        self.assertIn("flock -n 9", script)
        self.assertIn('mktemp -d "$BACKUP_ROOT/install-', script)
        self.assertIn("trap rollback_install EXIT HUP INT TERM", script)
        self.assertIn("managed-files.tar", script)
        self.assertIn("collector_active", script)
        self.assertIn("collector_enabled", script)
        self.assertIn("collector_present", script)
        self.assertIn("maintenance_timer_enabled", script)
        self.assertIn("vnstat_present", script)
        self.assertIn("sysstat_present", script)
        self.assertIn('DATABASE_ROLLBACK=$(find "$ROLLBACK_DIR/database"', script)
        self.assertIn('rm -f "$database" "$database-wal" "$database-shm"', script)
        self.assertIn("DATABASE_FILES_SAVED=true", script)
        self.assertIn('cp -a "$STATE_DIR/audit.db$suffix"', script)
        self.assertIn('cp -a "$ROLLBACK_DIR/database-files/audit.db$suffix"', script)
        self.assertIn('VNA_OPERATIONS_LOCK_HELD=1 "$DIST_DIR/node-audit-maintenance"', script)
        stop = 'systemctl stop node-audit-collector.service'
        backup = 'backup --dir "$ROLLBACK_DIR/database"'
        binary_install = 'install -o root -g root -m 0755 "$DIST_DIR/$binary"'
        self.assertLess(script.index(stop, script.index("# Freeze the old writer")), script.index(backup))
        self.assertLess(script.index(backup), script.index(binary_install))
        self.assertIn('if [ "$present_state" != true ]', script)
        self.assertIn('"$SYSTEMD_DIR/multi-user.target.wants/node-audit-collector.service"', script)
        self.assertIn('"$SYSTEMD_DIR/timers.target.wants/node-audit-maintenance.timer"', script)

    def test_restore_validates_artifact_and_has_global_rollback(self) -> None:
        script = self.read("restore.sh")
        validation = 'integrity --database "$BACKUP"'
        stop = 'systemctl stop "$COLLECTOR"'
        self.assertLess(script.index(validation), script.index(stop))
        self.assertIn("flock -n 9", script)
        self.assertIn('mktemp -d "$ROOT/var/backups/vps-node-auditor/pre-restore-', script)
        self.assertIn("trap restore_original EXIT HUP INT TERM", script)
        self.assertIn('cp -a "$ROLLBACK/current.db$suffix" "$DATABASE$suffix"', script)
        self.assertIn('if [ "$COLLECTOR_WAS_ACTIVE" = true ]', script)

    def test_uninstall_rejects_unknown_single_option(self) -> None:
        script = self.read("uninstall.sh")
        self.assertIn("1:--purge", script)
        self.assertIn('*) echo "only --purge is supported"', script)

    def test_obsolete_reload_assets_are_absent(self) -> None:
        self.assertFalse((ROOT / "deploy/scripts/verify-sing-box-reload.sh").exists())
        self.assertFalse((ROOT / "deploy/systemd/sing-box-reload.conf").exists())


if __name__ == "__main__":
    unittest.main()
