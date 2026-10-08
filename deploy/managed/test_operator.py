import importlib.util
import json
import os
from pathlib import Path
import tempfile
import sys
import subprocess
import unittest
from unittest.mock import patch
from types import SimpleNamespace

sys.path.insert(0, str(Path(__file__).parent))

spec = importlib.util.spec_from_file_location("juex_operator", Path(__file__).with_name("operator.py"))
ops = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ops)


class RecoveryTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)

    def test_direct_operator_entrypoint_uses_standard_library_operator(self):
        result = subprocess.run([sys.executable, "-S", str(Path(__file__).with_name("operator.py")), "--help"], capture_output=True)
        self.assertEqual(result.returncode, 0, result.stderr.decode())

    def test_unknown_backend_never_falls_back(self):
        with self.assertRaisesRegex(ValueError, "unknown deployment backend"):
            ops.backend({"backend": "unavailable"})

    def test_restore_refuses_corrupt_or_mismatched_pairs_before_creating_state(self):
        data, keys = self.root / "data" / "juex-test", self.root / "keys" / "juex-test"
        data.mkdir(parents=True)
        keys.mkdir(parents=True)
        (data / "database.dump").write_bytes(b"database")
        (keys / "secrets.tar").write_bytes(b"secrets")
        ops.write_json(data / "manifest.json", {"format": 1, "complete": True, "backup_id": "juex-test",
            "backend": "host", "files": {"database.dump": ops.digest(data / "database.dump")},
            "recovery_sha256": "different-key-archive"})
        ops.write_json(keys / "manifest.json", {"format": 1, "complete": True, "backup_id": "juex-test",
            "files": {"secrets.tar": ops.digest(keys / "secrets.tar")}})
        args = SimpleNamespace(backup=str(data.resolve()), recovery=str(keys.resolve()))
        with patch.object(ops.host, "restore") as restore:
            with self.assertRaisesRegex(ValueError, "does not match"):
                ops.restore(args)
            (data / "database.dump").write_bytes(b"corrupt")
            with self.assertRaisesRegex(ValueError, "checksum"):
                ops.restore(args)
            restore.assert_not_called()

    def test_failed_health_check_keeps_admission_closed(self):
        (self.root / "maintenance").mkdir()
        marker = self.root / "maintenance/draining"
        marker.write_text("upgrade")
        config = {"root": str(self.root), "backend": "host"}
        with patch.object(ops.host, "up"), patch.object(ops.host, "healthy", side_effect=RuntimeError("gateway failed")):
            with self.assertRaisesRegex(RuntimeError, "gateway failed"):
                ops.resume(config, "")
        self.assertTrue(marker.exists())

    def test_failed_revocation_can_refresh_review_without_another_mutation(self):
        (self.root / "maintenance").mkdir()
        required = self.root / "maintenance/recovery-required.json"
        required.write_text(json.dumps({"backup_id":"backup", "review_sha256":"pending-review"}))
        with patch.object(ops, "inventory", return_value={"ready":True,"services":[]}) as inventory:
            fingerprint = ops.review_recovery({"root":str(self.root)})
            inventory.assert_called_once_with({"root":str(self.root)}, offline=True)
        self.assertEqual(json.loads(required.read_text())["review_sha256"],fingerprint)
        self.assertEqual(len(fingerprint),64)

    def test_recovery_up_never_opens_gateway_before_review(self):
        (self.root / "maintenance").mkdir()
        (self.root / "maintenance/recovery-required.json").write_text('{}')
        with patch.object(ops.hosted, "compose") as compose:
            with self.assertRaisesRegex(ValueError, "review required"):
                ops.up({"root": str(self.root)})
            compose.assert_not_called()

    def test_foreign_compose_deployment_fails_before_start(self):
        def docker(_, *args, **kwargs):
            if args[0] == "ps": return SimpleNamespace(stdout=b"old\n")
            return SimpleNamespace(stdout=json.dumps([{"Config": {"Labels": {
                "com.docker.compose.project.config_files": "/old/compose.yaml"}}}]).encode())
        with patch.object(ops.hosted, "docker", side_effect=docker), patch.object(ops.hosted, "firewall") as firewall:
            with self.assertRaisesRegex(ValueError, "another juex deployment"):
                ops.up({"root": str(self.root)})
            firewall.assert_not_called()

    def test_hosted_initialization_preserves_requested_thread_limit(self):
        args = SimpleNamespace(root=str((self.root / "deployment").resolve()),
            workspace=str((self.root / "workspace").resolve()), public_url="https://example.test",
            platform_prefix="172.30.0", hosted_pool="172.31.0.0/16", listen_port=None,
            host_ip="172.30.0.1", dns=["1.1.1.1"], docker_socket=str((self.root / "docker.sock").resolve()),
            image="platform", hosted_image="guest", postgres_image="postgres", gateway_image="gateway",
            active_threads=1, ingress="https", proxy_cidr=[], local_tls=False,
            tls_certificate="cert.pem", tls_key="key.pem", tls_ca=None)
        # Inspect configuration at the ownership boundary, before any privileged
        # filesystem or container changes are allowed.
        with patch.object(ops.hosted, "mount_identity", return_value="workspace-uuid"), \
                patch.object(ops.hosted, "docker", return_value=SimpleNamespace(stdout=b"sha256:image\n")), \
                patch.object(ops.hosted, "assert_deployment_owner", side_effect=RuntimeError("stop before creation")) as ownership:
            with self.assertRaisesRegex(RuntimeError, "stop before creation"):
                ops.hosted.initialize(args)
        self.assertEqual(ownership.call_args.args[0]["active_threads"], 1)
        self.assertFalse(Path(args.root).exists())

    def test_recovery_preserves_operator_settings(self):
        (self.root / "secrets").mkdir()
        config = dict(root=str(self.root), workspace="/new-workspace", socket="/new.sock",
            platform_prefix="172.30.0", public_url="https://example.test", image="sha256:platform",
            hosted_image="sha256:hosted", postgres_image="sha256:pg", gateway_image="sha256:nginx",
            https_port=443, active_threads=10, storage_identity="new-uuid", hosted_pool="172.31.0.0/16",
            host_ip="10.0.2.100", dns=["1.1.1.1"])
        with patch.object(ops.hosted, "mount_device", return_value="/dev/loop7"):
            ops.hosted.render(config, "password", "key")
            env = self.root / "secrets/management.env"
            for service in ("management", "execution"):
                self.assertIn("JUEX_TRUSTED_PROXIES=172.30.0.11\n",
                              (self.root / "secrets" / (service + ".env")).read_text())
            env.write_text(env.read_text() + "JUEX_SMTP_CONFIG=encrypted-smtp-settings\n")
            hosted = json.loads((self.root / "hosted.json").read_text())
            hosted["backend"]["allow"] = ["192.168.1.0/24"]
            hosted["memory_bytes"], hosted["idle_seconds"] = 123456, 900
            (self.root / "hosted.json").write_text(json.dumps(hosted))
            config["host_ip"] = "10.0.2.101"
            config["hosted_image"] = "sha256:new-hosted"
            config["platform_prefix"] = "172.29.5"
            ops.hosted.render(config, "password", "key", preserve=True)
        self.assertIn("JUEX_SMTP_CONFIG=encrypted-smtp-settings\n", env.read_text())
        for service in ("management", "execution"):
            rendered = (self.root / "secrets" / (service + ".env")).read_text()
            self.assertIn("JUEX_TRUSTED_PROXIES=172.29.5.11\n", rendered)
            self.assertNotIn("JUEX_TRUSTED_PROXIES=172.30.0.11", rendered)
        restored = json.loads((self.root / "hosted.json").read_text())
        self.assertEqual(restored["backend"]["allow"], ["192.168.1.0/24"])
        self.assertEqual((restored["memory_bytes"], restored["idle_seconds"]), (123456,900))
        self.assertEqual(restored["backend"]["control"], "10.0.2.101:8684")
        self.assertEqual(restored["backend"]["image"], "sha256:new-hosted")

    def test_retention_counts_pairs_not_orphan_recovery_material(self):
        data,keys=self.root/"data",self.root/"keys"
        data.mkdir();keys.mkdir()
        for number in (1,2,3,4):
            name=f"juex-{number}"
            (keys/name).mkdir()
            (keys/name/"manifest.json").write_text(json.dumps({"format":1,"complete":True,"backup_id":name,"files":{"secrets.tar":"digest"}}))
            if number != 3:
                (data/name).mkdir()
                (data/name/"manifest.json").write_text(json.dumps({"format":1,"complete":True,"backup_id":name,"recovery_sha256":"digest"}))
        ops.prune_successful(data,keys,2)
        self.assertEqual(sorted(p.name for p in data.iterdir()),["juex-2","juex-4"])
        self.assertTrue((keys/"juex-2").exists())
        self.assertTrue((keys/"juex-4").exists())
        self.assertFalse((keys/"juex-1").exists())

    def test_hosted_mount_ownership_and_background_process(self):
        root=self.root
        item={"Id":"container","Config":{"Labels":{"ai.juex.hosted.environment":"env"}},
              "Mounts":[{"Destination":dest,"Source":str(source)} for dest,source in (
                  ("/var/lib/juex-control",root/"control/env/control"),
                  ("/workspace",root/"workspace/env/workspace"),("/home/agent",root/"workspace/env/home"))],
              "State":{"Running":True}}
        def docker(_, *args, **kwargs):
            if args[0]=="ps":return SimpleNamespace(stdout=b"container\n")
            if args[0]=="inspect":return SimpleNamespace(stdout=json.dumps([item]).encode())
            return SimpleNamespace(stdout=b"UID PID COMMAND\n0 1 juex-guest\n1000 20 sleep 600\n")
        with patch.object(ops.hosted,"docker",side_effect=docker):
            with self.assertRaisesRegex(RuntimeError,"user processes"):
                ops.hosted.hosted_containers({"root":str(root),"workspace":str(root/"workspace")})
        item["State"]["Running"]=False
        with patch.object(ops.hosted,"docker",side_effect=docker):
            self.assertEqual(len(ops.hosted.hosted_containers({"root":str(root),"workspace":str(root/"workspace")})),1)

    def test_hosted_management_inherits_only_explicit_operator_credentials(self):
        for environment in ({}, {"JUEX_MODEL_API_KEY": "model-secret", "JUEX_SMTP_CREDENTIAL": "smtp-secret"}):
            with self.subTest(environment=list(environment)), patch.dict(os.environ, environment, clear=True), \
                    patch.object(ops.hosted, "compose") as compose:
                ops.hosted.management({}, "models", "put")
                arguments = compose.call_args.args[1:]
                expected = ["run", "--rm", "--no-deps", "-T"]
                for name in environment:
                    expected.extend(["-e", name])
                self.assertEqual(arguments, (*expected, "operator", "juex-management", "models", "put"))

    def test_hosted_upgrade_removes_only_clean_owned_guests_before_replacing_binary(self):
        config = {"root": str(self.root), "workspace": str(self.root / "workspace")}
        for name in ("secrets", "control/env/control", "workspace/env/workspace", "workspace/env/home"):
            (self.root / name).mkdir(parents=True)
        for name in ("postgres-password", "master-key"):
            (self.root / "secrets" / name).write_text("test-only")
        proof = self.root / "workspace/env/home/keep"
        proof.write_text("durable")
        owned = {"Id": "owned", "Config": {"Labels": {"ai.juex.hosted.environment": "env"}},
                 "Mounts": [{"Destination": target, "Source": str(self.root / source)} for target, source in (
                     ("/var/lib/juex-control", "control/env/control"), ("/workspace", "workspace/env/workspace"),
                     ("/home/agent", "workspace/env/home"))],
                 "State": {"Running": False, "OOMKilled": False, "ExitCode": 143}}
        foreign = {"Id": "foreign", "Config": owned["Config"], "Mounts": [
            {"Destination": "/var/lib/juex-control", "Source": "/another/control/env/control"}],
            "State": {"Running": False}}
        calls = []
        def docker(_, *args, **kwargs):
            calls.append(args)
            if args[0] == "ps": return SimpleNamespace(stdout=b"owned foreign\n")
            if args[:2] == ("inspect", "--format"):
                return SimpleNamespace(stdout=json.dumps(owned["State"]).encode())
            if args[0] == "inspect":
                return SimpleNamespace(stdout=json.dumps([owned if args[1] == "owned" else foreign]).encode())
            if args[0] == "create": return SimpleNamespace(stdout=b"image-copy\n")
            return SimpleNamespace(stdout=b"sha256:replacement\n")
        args = SimpleNamespace(image="new-platform", hosted_image="new-guest")
        with patch.object(ops.hosted, "docker", side_effect=docker), patch.object(ops.hosted, "render"):
            ops.hosted.upgrade(config, args)
        self.assertIn(("rm", "owned"), calls)
        self.assertLess(calls.index(("rm", "owned")), next(i for i, call in enumerate(calls) if call[0] == "cp"))
        self.assertFalse(any(call[0] in ("rm", "network") and "foreign" in call for call in calls))
        self.assertFalse(any("-f" in call or "-v" in call or call[0] == "network" for call in calls))
        self.assertEqual(proof.read_text(), "durable")
        for state in ({"Running": True, "ExitCode": 0}, {"Running": False, "OOMKilled": True, "ExitCode": 0},
                      {"Running": False, "ExitCode": 137}):
            calls.clear()
            with self.subTest(state=state), patch.object(ops.hosted, "hosted_containers", return_value=[owned]), \
                    patch.object(ops.hosted, "docker", side_effect=docker), patch.object(ops.hosted, "render") as render:
                owned["State"] = state
                with self.assertRaisesRegex(RuntimeError, "cleanly"):
                    ops.hosted.upgrade(config, args)
                self.assertFalse(any(call[0] in ("rm", "cp", "create") for call in calls))
                render.assert_not_called()


if __name__ == "__main__":
    unittest.main()
