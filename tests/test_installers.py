"""Exercise installer trust boundaries against a loopback release fixture."""

import hashlib
import http.server
import io
import os
import pathlib
import platform
import subprocess
import sys
import tarfile
import tempfile
import threading
import unittest
import zipfile


ROOT = pathlib.Path(__file__).resolve().parents[1]
VERSION = "0.1.2"
WINDOWS = os.name == "nt"


class FixtureServer(http.server.ThreadingHTTPServer):
    def __init__(self, assets):
        self.assets = assets
        super().__init__(("127.0.0.1", 0), FixtureHandler)


class FixtureHandler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        body = self.server.assets.get(self.path)
        if body is None:
            self.send_error(404)
            return
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, _format, *_args):
        pass


def archive_for(entries):
    data = io.BytesIO()
    if WINDOWS:
        with zipfile.ZipFile(data, "w", compression=zipfile.ZIP_DEFLATED) as archive:
            for name, content in entries:
                archive.writestr(name, content)
    else:
        with tarfile.open(fileobj=data, mode="w:gz") as archive:
            for name, content in entries:
                entry = tarfile.TarInfo(name)
                entry.size = len(content)
                entry.mode = 0o755 if name == "runnerctl" else 0o644
                archive.addfile(entry, io.BytesIO(content))
    return data.getvalue()


class InstallerTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="runnerctl-installer-test-")
        self.addCleanup(self.temp.cleanup)
        self.home = pathlib.Path(self.temp.name) / "home"
        self.home.mkdir()
        self.target = (self.home / "Programs" / "runnerctl" / "runnerctl.exe") if WINDOWS else (self.home / ".local" / "bin" / "runnerctl")
        if WINDOWS:
            arch = "arm64" if platform.machine().lower() in ("arm64", "aarch64") else "amd64"
            self.name = f"runnerctl_{VERSION}_windows_{arch}.zip"
            self.executable = b"MZ-fake-test-binary"
            self.good_entries = [("LICENSE", b"license"), ("README.md", b"readme"), ("SECURITY.md", b"security"), ("VERSION", b"0.1.2\n"), ("runnerctl.exe", self.executable)]
        else:
            system = "darwin" if sys.platform == "darwin" else "linux"
            arch = "arm64" if platform.machine().lower() in ("arm64", "aarch64") else "amd64"
            self.name = f"runnerctl_{VERSION}_{system}_{arch}.tar.gz"
            self.executable = b"#!/bin/sh\necho fixture\n"
            self.good_entries = [("LICENSE", b"license"), ("README.md", b"readme"), ("SECURITY.md", b"security"), ("VERSION", b"0.1.2\n"), ("runnerctl", self.executable)]

    def run_installer(self, entries=None, tamper=False, extra_env=None):
        archive = archive_for(entries or self.good_entries)
        digest = hashlib.sha256(archive).hexdigest()
        if tamper:
            digest = "0" * 64
        base = f"/downloads/runnerctl/{VERSION}"
        assets = {
            "/downloads/runnerctl/latest/VERSION": f"{VERSION}\n".encode(),
            f"{base}/SHA256SUMS": f"{digest}  {self.name}\n".encode(),
            f"{base}/{self.name}": archive,
        }
        server = FixtureServer(assets)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            env = os.environ.copy()
            env["RUNNERCTL_INSTALL_TEST_MODE"] = "1"
            env["RUNNERCTL_INSTALL_TEST_BASE_URL"] = f"http://127.0.0.1:{server.server_port}"
            if WINDOWS:
                env["LOCALAPPDATA"] = str(self.home)
                script = str(ROOT / "scripts" / "install.ps1").replace("'", "''")
                command = ["powershell", "-NoProfile", "-Command", f"Get-Content -Raw -LiteralPath '{script}' | Invoke-Expression"]
            else:
                env["HOME"] = str(self.home)
                command = ["sh", str(ROOT / "scripts" / "install.sh")]
            if extra_env:
                env.update(extra_env)
            return subprocess.run(command, env=env, capture_output=True, text=True, timeout=20)
        finally:
            server.shutdown()
            server.server_close()
            thread.join(timeout=5)

    def test_installs_and_replaces_current_user_binary(self):
        first = self.run_installer()
        self.assertEqual(first.returncode, 0, first.stderr)
        self.assertEqual(self.target.read_bytes(), self.executable)
        self.target.write_bytes(b"old")
        second = self.run_installer()
        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertEqual(self.target.read_bytes(), self.executable)

    def test_rejects_tampered_archive_before_install(self):
        result = self.run_installer(tamper=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.target.exists())

    def test_rejects_traversal_even_with_matching_digest(self):
        entries = self.good_entries + [("../outside", b"bad")]
        result = self.run_installer(entries=entries)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.target.exists())
        self.assertFalse((self.home / "outside").exists())

    def test_rejects_unsupported_architecture(self):
        key = "RUNNERCTL_INSTALL_TEST_ARCH"
        result = self.run_installer(extra_env={key: "mips64"})
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.target.exists())

    def test_rejects_oversized_binary(self):
        binary_name = "runnerctl.exe" if WINDOWS else "runnerctl"
        entries = [(name, b"\0" * (67108864 + 1) if name == binary_name else content)
                   for name, content in self.good_entries]
        result = self.run_installer(entries=entries)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.target.exists())

    @unittest.skipIf(WINDOWS, "Unix directory permissions")
    def test_rejects_shared_install_directory(self):
        self.target.parent.mkdir(parents=True)
        for mode in (0o775, 0o702):
            with self.subTest(mode=oct(mode)):
                self.target.parent.chmod(mode)
                result = self.run_installer()
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(self.target.exists())

    @unittest.skipUnless(WINDOWS, "Windows ACLs")
    def test_rejects_writable_windows_directory(self):
        self.target.parent.mkdir(parents=True)
        subprocess.run(
            ["icacls", str(self.target.parent), "/grant", "*S-1-1-0:(OI)(CI)M"],
            check=True,
            capture_output=True,
        )
        result = self.run_installer()
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.target.exists())


if __name__ == "__main__":
    unittest.main()
