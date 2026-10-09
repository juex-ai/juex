import json
from pathlib import Path
import subprocess
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).parent))
import host
import hosted
import common


class IngressTests(unittest.TestCase):
    def args(self, **values):
        return SimpleNamespace(**dict(dict(ingress="https", public_url="https://juex.example.test",
            listen_port=None, proxy_cidr=[], local_tls=False, tls_certificate="cert.pem",
            tls_key="key.pem", tls_ca=None), **values))

    def test_http_requires_explicit_mode_and_rejects_ignored_certificates(self):
        with self.assertRaises(ValueError):
            common.ingress_configuration(self.args(public_url="http://machine:8080"))
        with self.assertRaises(ValueError):
            common.ingress_configuration(self.args(ingress="http", public_url="http://machine:8080"))
        value = common.ingress_configuration(self.args(ingress="http", public_url="http://machine:8080",
                                               tls_certificate=None, tls_key=None))
        self.assertEqual(value["https_port"], 8080)
        self.assertEqual(value["ingress"], "http")
        with self.assertRaises(ValueError):
            common.ingress_configuration(self.args(ingress="http", public_url="https://machine:8080",
                                            tls_certificate=None, tls_key=None))

    def test_external_tls_requires_https_an_explicit_hop_and_proxy_scope(self):
        args = self.args(ingress="proxy", tls_certificate=None, tls_key=None)
        with self.assertRaises(ValueError):
            common.ingress_configuration(args)
        args.listen_port = 8080
        with self.assertRaises(ValueError):
            common.ingress_configuration(args)
        args.proxy_cidr = ["127.0.0.1/32"]
        value = common.ingress_configuration(args)
        self.assertEqual(value["proxy_cidrs"], ["127.0.0.1/32"])
        for origin in ("https://machine:invalid", "https://machine:0", "https://machine:65536"):
            with self.subTest(origin=origin), self.assertRaises(ValueError):
                common.ingress_configuration(self.args(ingress="proxy", public_url=origin,
                    tls_certificate=None, tls_key=None, listen_port=8080, proxy_cidr=["127.0.0.1/32"]))
        for invalid in ("0.0.0.0/0", "not-an-ip; deny all", "::/0"):
            args.proxy_cidr = [invalid]
            with self.subTest(invalid=invalid), self.assertRaises(ValueError):
                common.ingress_configuration(args)

    def test_gateway_renders_http_and_restricted_external_tls(self):
        template = (host.HERE / "nginx.conf").read_text()
        http = common.ingress_gateway({"ingress": "http"}, template)
        self.assertNotIn("ssl_certificate", http)
        self.assertNotIn("443 ssl", http)
        self.assertIn("X-Forwarded-Proto http;", http)
        proxy = common.ingress_gateway({"ingress": "proxy", "proxy_cidrs": ["127.0.0.1/32"]}, template)
        self.assertNotIn("ssl_certificate", proxy)
        self.assertIn("X-Forwarded-Proto https;", proxy)
        self.assertIn("geo $realip_remote_addr", proxy)
        self.assertIn("set_real_ip_from 127.0.0.1/32;", proxy)
        self.assertIn("real_ip_header X-Real-IP;", proxy)
        self.assertIn("return 403;", proxy)

    def test_host_http_reaches_management_and_native_enrollment(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "secrets").mkdir()
            config = dict(root=str(root), ingress="http", rpc_base=19741,
                public_url="http://machine:19644", workspace="/workspaces", identity="test",
                http_port=19640, device_port=19643, https_port=19644, path="/bin",
                release={"bin": "/release/bin", "operator": str(host.HERE)})
            host.render(config, "password", "key")
            backend = json.loads((root / "host.json").read_text())["backend"]
            self.assertTrue(backend["insecure_http"])
            self.assertEqual(backend["ca_file"], "")
            self.assertIn("--insecure-http", host.service_command(config, "management"))
            with patch.object(host, "run") as run:
                host.management(config, "bootstrap", "--email", "test@example.test")
                self.assertIn("--insecure-http", run.call_args.args[0])
            self.assertNotIn("ssl_certificate", (root / "nginx.conf").read_text())

    def test_management_operator_only_disables_secure_cookies_for_http(self):
        for mode, expected in (("http", True), ("https", False), ("proxy", False)):
            with self.subTest(mode=mode), patch.object(hosted, "compose") as compose:
                hosted.management({"ingress": mode}, "bootstrap")
                self.assertEqual("--insecure-http" in compose.call_args.args, expected)

    def test_hosted_private_health_cannot_hide_failed_public_ingress(self):
        with patch.object(hosted, "compose", return_value=SimpleNamespace(returncode=0)), \
                patch.object(common, "ingress_healthy", return_value=False), \
                patch.object(hosted.time, "monotonic", side_effect=[0, 91]):
            with self.assertRaisesRegex(RuntimeError, "public gateway"):
                hosted.healthy({"ingress": "http"})

    def test_slow_private_probe_retries_within_startup_window(self):
        for backend, probe in ((host, "management"), (hosted, "compose")):
            with self.subTest(backend=backend.__name__), \
                    patch.object(backend, probe, side_effect=[subprocess.TimeoutExpired("services check", 15), SimpleNamespace(returncode=0)]) as check, \
                    patch.object(common, "ingress_healthy", return_value=True) as ingress, \
                    patch.object(backend.time, "monotonic", side_effect=[0, 15]), \
                    patch.object(backend.time, "sleep"):
                backend.healthy({"ingress": "http"})
                self.assertEqual(check.call_count, 2)
                ingress.assert_called_once()

    def test_timed_out_private_probe_cannot_bypass_startup_deadline(self):
        for backend, probe in ((host, "management"), (hosted, "compose")):
            with self.subTest(backend=backend.__name__), \
                    patch.object(backend, probe, side_effect=subprocess.TimeoutExpired("services check", 15)), \
                    patch.object(common, "ingress_healthy") as ingress, \
                    patch.object(backend.time, "monotonic", side_effect=[0, 91]):
                with self.assertRaisesRegex(RuntimeError, "maintenance retained"):
                    backend.healthy({"ingress": "http"})
                ingress.assert_not_called()

    def test_proxy_requires_realip_module_before_creating_state(self):
        with self.assertRaisesRegex(ValueError, "http_realip_module"):
            common.ingress_check_nginx({"ingress": "proxy"}, b"nginx --with-http_ssl_module")
        common.ingress_check_nginx({"ingress": "proxy"}, b"nginx --with-http_realip_module")


if __name__ == "__main__":
    unittest.main()
