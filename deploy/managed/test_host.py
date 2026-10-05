import importlib.util
import json
import os
from pathlib import Path
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).parent))
import host


class HostDeploymentTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.base = Path(self.tmp.name).resolve()

    def release(self):
        directory = self.base / "source"
        directory.mkdir()
        for name in host.BINARIES:
            path = directory / name
            path.write_text("#!/bin/sh\nexit 0\n")
            path.chmod(0o755)
        return directory

    def test_init_does_not_adopt_any_existing_directory(self):
        root = self.base / "foreign"
        root.mkdir()
        (root / "important").write_text("unchanged")
        with self.assertRaisesRegex(ValueError, "new deployment"):
            host.initialize(SimpleNamespace(root=str(root)))
        self.assertEqual(list(root.iterdir()), [root / "important"])

    def test_release_refuses_partial_or_linked_binaries_before_installing(self):
        source = self.release()
        root = self.base / "deployment"
        root.mkdir()
        (source / "juex-executor").unlink()
        with self.assertRaises(ValueError):
            host.stage_release(root, source)
        self.assertEqual(list(root.iterdir()), [])
        (source / "juex-executor").symlink_to(source / "juex-runtime")
        with self.assertRaises(ValueError):
            host.stage_release(root, source)
        self.assertEqual(list(root.iterdir()), [])

    def test_release_is_immutable_and_detects_tampering(self):
        source = self.release()
        root = self.base / "deployment"
        root.mkdir()
        release = host.stage_release(root, source)
        self.assertEqual(host.stage_release(root, source), release)
        config = {"root": str(root), "release": release}
        host.verify_release(config)
        installed = Path(release["bin"]) / "juex-runtime"
        installed.chmod(0o700)
        installed.write_text("changed")
        with self.assertRaisesRegex(ValueError, "checksum"):
            host.verify_release(config)

    def test_restore_paths_and_platform_checked_before_writes(self):
        config = {"root": "/old/deployment", "workspace": "/old/workspace",
                  "os": host.system(), "arch": host.machine()}
        with self.assertRaisesRegex(ValueError, "same canonical"):
            host.validate_restore(config, str(self.base / "new"), "/old/workspace")
        config["arch"] = "other"
        with self.assertRaisesRegex(ValueError, "architecture"):
            host.validate_restore(config, "/old/deployment", "/old/workspace")
        self.assertEqual(list(self.base.iterdir()), [])

    def test_operator_assets_belong_to_the_release_and_upgrade_together(self):
        source = self.release()
        root = self.base / "deployment"
        root.mkdir()
        assets = source.parent / "deploy/managed"
        assets.mkdir(parents=True)
        for name in host.OPERATOR_FILES:
            (assets / name).write_text("original " + name)
        first = host.stage_release(root, source)
        (root / "operator").mkdir()
        config = {"release": first, "operator": str(root / "operator")}
        host.install_operator(config)
        (assets / "host.py").write_text("updated host operator")
        second = host.stage_release(root, source)
        self.assertNotEqual(first["id"], second["id"])
        host.install_operator(dict(config, release=second))
        self.assertEqual((root / "operator/host.py").read_text(), "updated host operator")
        host.verify_release(config)

    def test_read_only_status_never_creates_runtime_files(self):
        config = {"root": str(self.base), "identity": "test", "os": "darwin",
                  "release": {"id": "version"}}
        before = sorted(self.base.rglob("*"))
        with patch.object(host, "service_status", return_value={"running": False}):
            result = host.status(config)
        self.assertEqual(result["backend"], "host")
        self.assertEqual(before, sorted(self.base.rglob("*")))

    def test_upgrade_preserves_host_policy_and_tls_identity(self):
        root = self.base
        (root / "secrets").mkdir()
        config = {"root": str(root), "rpc_base": 19741, "public_url": "https://example.test:19644",
                  "workspace": "/workspaces", "identity": "identity", "local_tls": True,
                  "http_port": 19640, "device_port": 19643, "https_port": 19644,
                  "release": {"bin": "/release/bin", "operator": str(host.HERE)}}
        host.render(config, "password", "master")
        saved = json.loads((root / "host.json").read_text())
        saved["idle_seconds"] = 900
        saved["backend"].update(ca_file="/private/custom-ca.pem", server_name="custom.test")
        (root / "host.json").write_text(json.dumps(saved))
        config["release"]["bin"] = "/new-release/bin"
        host.render(config, "password", "master", preserve=True)
        updated = json.loads((root / "host.json").read_text())
        self.assertEqual(updated["idle_seconds"], 900)
        self.assertEqual(updated["backend"]["ca_file"], "/private/custom-ca.pem")
        self.assertEqual(updated["backend"]["server_name"], "custom.test")
        self.assertEqual(updated["backend"]["executable"], "/new-release/bin/juex-executor")
        nginx = (root / "nginx.conf").read_text()
        for name in ("client_body", "proxy", "fastcgi", "uwsgi", "scgi"):
            self.assertIn(f'{name}_temp_path "{root}/run/nginx-{name}";', nginx)

    def test_private_rpc_health_does_not_hide_failed_public_gateway(self):
        config = {"root": str(self.base), "public_url": "https://unavailable.test"}
        with patch.object(host, "management", return_value=SimpleNamespace(returncode=0)), \
                patch.object(host.urllib.request, "urlopen", side_effect=OSError("gateway unavailable")), \
                patch.object(host.time, "monotonic", side_effect=[0, 91]):
            with self.assertRaisesRegex(RuntimeError, "public HTTPS gateway"):
                host.healthy(config)


if __name__ == "__main__":
    unittest.main()
