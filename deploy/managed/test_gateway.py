"""Real proxy transport check; run with JUEX_TEST_NGINX and JUEX_TEST_CADDY."""

import http.client
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import socket
import ssl
import subprocess
import sys
import tempfile
import threading
import time
import unittest

sys.path.insert(0, str(Path(__file__).parent))
import host


@unittest.skipUnless(os.environ.get("JUEX_TEST_NGINX") and os.environ.get("JUEX_TEST_CADDY"),
                     "real gateway check requires JUEX_TEST_NGINX and JUEX_TEST_CADDY")
class GatewayTests(unittest.TestCase):
    def test_caddy_nginx_preserve_clients_and_reject_untrusted_hops(self):
        class Echo(BaseHTTPRequestHandler):
            def do_GET(self):
                if self.path == "/device/socket":
                    self.send_response(101)
                    self.send_header("Upgrade", self.headers["Upgrade"])
                    self.send_header("Connection", self.headers["Connection"])
                else:
                    self.send_response(200)
                self.end_headers()
                if self.path != "/device/socket":
                    self.wfile.write(json.dumps(dict(self.headers)).encode())

            def log_message(self, *_):
                pass

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            server = ThreadingHTTPServer(("127.0.0.1", 0), Echo)
            threading.Thread(target=server.serve_forever, daemon=True).start()
            self.addCleanup(server.server_close)
            self.addCleanup(server.shutdown)
            ports = []
            for _ in range(2):
                with socket.socket() as probe:
                    probe.bind(("0.0.0.0", 0))
                    ports.append(probe.getsockname()[1])
            gateway_port, public_port = ports
            for name in ("secrets", "run"):
                (root / name).mkdir()
            config = dict(root=str(root), ingress="proxy", proxy_cidrs=["127.0.0.1/32"],
                rpc_base=19741, public_url=f"https://127.0.0.1:{public_port}", workspace="/unused",
                identity="gateway-test", http_port=server.server_port, device_port=server.server_port,
                https_port=gateway_port, release={"bin": "/unused", "operator": str(host.HERE)})
            host.render(config, "unused", "unused")
            subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1",
                "-keyout", str(root / "key.pem"), "-out", str(root / "cert.pem"),
                "-subj", "/CN=localhost", "-addext", "subjectAltName=IP:127.0.0.1"],
                capture_output=True, check=True)
            (root / "Caddyfile").write_text(f'''{{
  admin off
  auto_https off
  skip_install_trust
}}
https://127.0.0.1:{public_port} {{
  tls {root}/cert.pem {root}/key.pem
  reverse_proxy 127.0.0.1:{gateway_port} {{
    header_up X-Real-IP {{remote_host}}
  }}
}}
''')
            processes = []
            try:
                with (root / "proxy.log").open("w+") as log:
                    for command in ([os.environ["JUEX_TEST_NGINX"], "-p", str(root), "-c", str(root / "nginx.conf"), "-g", "daemon off;"],
                                    [os.environ["JUEX_TEST_CADDY"], "run", "--config", str(root / "Caddyfile"), "--adapter", "caddyfile"]):
                        processes.append(subprocess.Popen(command, stdout=log, stderr=log,
                            env=dict(os.environ, XDG_DATA_HOME=str(root / "data"), XDG_CONFIG_HOME=str(root / "config"))))
                    context = ssl.create_default_context(cafile=str(root / "cert.pem"))

                    def request(path, tls=True):
                        connection = (http.client.HTTPSConnection("127.0.0.1", public_port, context=context, timeout=3)
                                      if tls else http.client.HTTPConnection("127.0.0.1", gateway_port, timeout=3))
                        try:
                            connection.request("GET", path, headers={"X-Real-IP": "198.51.100.99", "X-Forwarded-Proto": "http",
                                "Upgrade": "websocket", "Connection": "upgrade"})
                            response = connection.getresponse()
                            return response.status, dict(response.getheaders()), response.read()
                        finally:
                            connection.close()

                    deadline = time.monotonic() + 10
                    while True:
                        try:
                            status, _, data = request("/api/probe")
                            break
                        except OSError:
                            if time.monotonic() >= deadline or any(p.poll() is not None for p in processes):
                                log.seek(0)
                                self.fail(log.read())
                            time.sleep(0.1)
                    self.assertEqual(status, 200)
                    headers = {key.lower(): value for key, value in json.loads(data).items()}
                    self.assertEqual(headers["x-real-ip"], "127.0.0.1")
                    self.assertEqual(headers["x-forwarded-proto"], "https")
                    status, headers, _ = request("/device/socket")
                    self.assertEqual(status, 101)
                    self.assertEqual(headers["Upgrade"].lower(), "websocket")
                    # A public gateway with no trusted loopback peer must deny
                    # the direct request regardless of its forged client header.
                    config["proxy_cidrs"] = ["192.0.2.1/32"]
                    host.render(config, "unused", "unused", preserve=True)
                    subprocess.run([os.environ["JUEX_TEST_NGINX"], "-p", str(root), "-c", str(root / "nginx.conf"), "-s", "reload"],
                                   capture_output=True, check=True)
                    deadline = time.monotonic() + 5
                    while request("/api/probe", tls=False)[0] != 403:
                        if time.monotonic() >= deadline:
                            self.fail("untrusted TCP peer was admitted")
                        time.sleep(0.1)
            finally:
                for process in reversed(processes):
                    if process.poll() is None:
                        process.terminate()
                    process.wait(timeout=10)


if __name__ == "__main__":
    unittest.main()
