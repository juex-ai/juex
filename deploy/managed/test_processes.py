import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).parent))
import processes


class ProcessTests(unittest.TestCase):
    def test_resume_clears_stop_marker_without_restarting_running_generation(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "run").mkdir()
            marker = root / "run/runtime.stop"
            marker.write_text("stop timed out")
            config = {"root": str(root), "release": {"id": "release"}}
            before = {"pid": 42, "fingerprint": "original", "release": "release"}
            with patch.object(processes, "assert_definition", return_value=root / "service.plist"), \
                    patch.object(processes, "service_status", return_value={"running": True, "pid": 42}), \
                    patch.object(processes, "receipt", return_value=before), \
                    patch.object(processes, "fingerprint", return_value="original"), \
                    patch.object(processes, "run") as run:
                processes.start(config, "runtime")
                self.assertFalse(marker.exists())
                run.assert_not_called()

    def test_stop_guard_does_not_overwrite_failed_original_receipt(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "run").mkdir()
            (root / "run/runtime.stop").write_text("operator stop")
            record = root / "run/runtime.json"
            record.write_text(json.dumps({"generation": "original", "exit_code": 1}))
            before = record.read_bytes()
            with patch.object(processes.subprocess, "Popen") as spawn:
                self.assertEqual(processes.serve({"root": directory}, "runtime", [], {}), 0)
                spawn.assert_not_called()
            self.assertEqual(record.read_bytes(), before)


if __name__ == "__main__":
    unittest.main()
