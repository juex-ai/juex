import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
from types import SimpleNamespace

spec = importlib.util.spec_from_file_location("juex_operator", Path(__file__).with_name("operator.py"))
ops = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ops)


class RecoveryTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)

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
        with patch.object(ops, "compose") as compose:
            with self.assertRaisesRegex(ValueError, "review required"):
                ops.up({"root": str(self.root)})
            compose.assert_not_called()

    def test_foreign_compose_deployment_fails_before_start(self):
        def docker(_, *args, **kwargs):
            if args[0] == "ps": return SimpleNamespace(stdout=b"old\n")
            return SimpleNamespace(stdout=json.dumps([{"Config": {"Labels": {
                "com.docker.compose.project.config_files": "/old/compose.yaml"}}}]).encode())
        with patch.object(ops, "docker", side_effect=docker), patch.object(ops, "firewall") as firewall:
            with self.assertRaisesRegex(ValueError, "another juex deployment"):
                ops.up({"root": str(self.root)})
            firewall.assert_not_called()

    def test_recovery_preserves_operator_settings(self):
        (self.root / "secrets").mkdir()
        config = dict(root=str(self.root), workspace="/new-workspace", socket="/new.sock",
            platform_prefix="172.30.0", public_url="https://example.test", image="sha256:platform",
            hosted_image="sha256:hosted", postgres_image="sha256:pg", gateway_image="sha256:nginx",
            https_port=443, active_threads=10, storage_identity="new-uuid", hosted_pool="172.31.0.0/16",
            host_ip="10.0.2.100", dns=["1.1.1.1"])
        with patch.object(ops, "mount_device", return_value="/dev/loop7"):
            ops.render(config, "password", "key")
            env = self.root / "secrets/management.env"
            env.write_text(env.read_text() + "JUEX_SMTP_CONFIG=encrypted-smtp-settings\n")
            hosted = json.loads((self.root / "hosted.json").read_text())
            hosted["backend"]["allow"] = ["192.168.1.0/24"]
            hosted["memory_bytes"], hosted["idle_seconds"] = 123456, 900
            (self.root / "hosted.json").write_text(json.dumps(hosted))
            config["host_ip"] = "10.0.2.101"
            ops.render(config, "password", "key", preserve=True)
        self.assertIn("JUEX_SMTP_CONFIG=encrypted-smtp-settings\n", env.read_text())
        restored = json.loads((self.root / "hosted.json").read_text())
        self.assertEqual(restored["backend"]["allow"], ["192.168.1.0/24"])
        self.assertEqual((restored["memory_bytes"], restored["idle_seconds"]), (123456,900))
        self.assertEqual(restored["backend"]["control"], "10.0.2.101:8684")

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
        with patch.object(ops,"docker",side_effect=docker):
            with self.assertRaisesRegex(RuntimeError,"user processes"):
                ops.hosted_containers({"root":str(root),"workspace":str(root/"workspace")})
        item["State"]["Running"]=False
        with patch.object(ops,"docker",side_effect=docker):
            self.assertEqual(len(ops.hosted_containers({"root":str(root),"workspace":str(root/"workspace")})),1)


if __name__ == "__main__":
    unittest.main()
