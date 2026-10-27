#!/usr/bin/env python3
"""Disposable rootless etcd integration checks. Python standard library only."""
import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import secrets
import shutil
import signal
import stat
import struct
import subprocess
import sys
import tempfile
import time
import uuid

DEFAULT_IMAGE = "gcr.io/etcd-development/etcd:v3.7.3"
ETCD = "/usr/local/bin/etcd"
CTL = "/usr/local/bin/etcdctl"
UTL = "/usr/local/bin/etcdutl"
SCENARIOS = (
    "official snapshot/restore", "TLS/mTLS/auth fixture", "version", "backup",
    "verify all and named", "restore product snapshot", "endpoint failover",
    "cluster mismatch preservation", "retention keep=2", "same-size tamper",
    "metadata/manifest corruption", "space failure preservation",
    "unreachable preservation", "wrong CA preservation", "wrong auth preservation",
    "cancellation preservation", "offline status/verify", "empty verify rejection",
)


class Failure(Exception):
    pass


def require(condition, message):
    if not condition:
        raise Failure(message)


def static_elf(path):
    """Reject dynamically linked ELF (PT_INTERP), without executing host tools."""
    with path.open("rb") as f:
        header = f.read(64)
        require(header[:4] == b"\x7fELF" and header[4] in (1, 2)
                and header[5] in (1, 2), "--binary must be a static Linux ELF")
        endian = "<" if header[5] == 1 else ">"
        if header[4] == 2:
            offset = struct.unpack_from(endian + "Q", header, 32)[0]
            size, count = struct.unpack_from(endian + "HH", header, 54)
        else:
            offset = struct.unpack_from(endian + "I", header, 28)[0]
            size, count = struct.unpack_from(endian + "HH", header, 42)
        require(size >= 4 and count < 65535, "unsupported ELF program header")
        for index in range(count):
            f.seek(offset + index * size)
            entry = f.read(4)
            require(len(entry) == 4, "truncated ELF program header")
            require(struct.unpack(endian + "I", entry)[0] != 3,
                    "--binary has PT_INTERP; supply a static executable")


class Harness:
    def __init__(self, args):
        self.args = args
        self.evidence = args.evidence_dir
        self.evidence.mkdir(mode=0o700, parents=True, exist_ok=True)
        self.log = (self.evidence / "integration.log").open("x", encoding="utf-8")
        self.root = Path(tempfile.mkdtemp(prefix="etcd-keeper-integration-fixtures-"))
        self.binary_sha256 = None
        self.image_id = None
        self.containers = []
        self.source = "ebk-source-" + uuid.uuid4().hex
        self.results = {}
        self.sensitive = []
        self.password = secrets.token_urlsafe(32)
        self.wrong_password = secrets.token_urlsafe(32)
        self.sensitive.extend([self.password, self.wrong_password])
        self.record("Evidence format: scenario outcomes only; no raw subprocess output or secrets.")
        self.record("Image: " + self.args.image)
        self.record("Mode: " + ("fixture-only (NOT product acceptance)" if args.fixture_only else "full"))

    def record(self, text):
        print(text, flush=True)
        self.log.write(text + "\n")
        self.log.flush()

    def passed(self, name):
        self.results[name] = "PASS"
        self.record("PASS " + name)

    def run(self, argv, *, timeout=30, input=None, expected=0, label="subprocess"):
        try:
            result = subprocess.run(argv, input=input, text=True, stdout=subprocess.PIPE,
                                    stderr=subprocess.PIPE, timeout=timeout, check=False)
        except subprocess.TimeoutExpired:
            raise Failure(label + " timed out") from None
        except OSError:
            raise Failure(label + " could not start") from None
        combined = result.stdout + result.stderr
        require(not any(secret in combined for secret in self.sensitive),
                label + " leaked a fixture password/private key (output suppressed)")
        require("-----BEGIN PRIVATE KEY-----" not in combined
                and "-----BEGIN RSA PRIVATE KEY-----" not in combined,
                label + " emitted private key material (output suppressed)")
        if expected is not None:
            require(result.returncode == expected,
                    label + " unexpected exit code " + str(result.returncode) + " (output suppressed)")
        return result

    def exec(self, *argv, container=None, **kwargs):
        command = ["podman", "exec"]
        if kwargs.get("input") is not None:
            command.append("-i")
        return self.run(command + [container or self.source, *argv], **kwargs)

    def ctl(self, *argv, tls=False, container=None, port=None, **kwargs):
        port = port or (2379 if tls else 12379)
        base = [CTL, "--endpoints=" + ("https" if tls else "http") + "://127.0.0.1:" + str(port),
                "--dial-timeout=2s", "--command-timeout=5s"]
        if tls:
            base += ["--cacert=/work/tls/ca.crt", "--cert=/work/tls/admin.crt",
                     "--key=/work/tls/admin.key"]
        return self.exec(*base, *argv, container=container, **kwargs)

    def start(self, name, directory, client, peer, *, tls=False):
        args = [ETCD, "--name=fixture", "--data-dir=" + directory,
                "--listen-client-urls=" + ("https" if tls else "http") + "://127.0.0.1:" + str(client),
                "--advertise-client-urls=" + ("https" if tls else "http") + "://127.0.0.1:" + str(client),
                "--listen-peer-urls=http://127.0.0.1:" + str(peer),
                "--initial-advertise-peer-urls=http://127.0.0.1:" + str(peer),
                "--initial-cluster=fixture=http://127.0.0.1:" + str(peer),
                "--initial-cluster-token=fixture-" + str(peer), "--log-level=error"]
        if tls:
            args += ["--cert-file=/work/tls/server.crt", "--key-file=/work/tls/server.key",
                     "--trusted-ca-file=/work/tls/ca.crt", "--client-cert-auth=true"]
        # No image entrypoint/shell is assumed; all processes are explicit binaries.
        self.containers.append(name)  # Register before starting, including timeout paths.
        self.run(["podman", "run", "--detach", "--pull=never", "--network=none",
                  "--name", name, "--volume", str(self.root) + ":/work:z",
                  "--entrypoint", ETCD, self.args.image, *args[1:]], label="start isolated etcd")
        self.ready(container=name, tls=tls, port=client)

    def ready(self, *, container=None, tls=False, port=None):
        deadline = time.monotonic() + 25
        while time.monotonic() < deadline:
            result = self.ctl("endpoint", "health", tls=tls, container=container, port=port,
                              timeout=6, expected=None, label="readiness")
            if result.returncode == 0:
                return
            time.sleep(0.2)
        raise Failure("isolated etcd readiness deadline exceeded")

    def certificates(self):
        tls = self.root / "tls"
        tls.mkdir(mode=0o700)
        self.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1",
                  "-subj", "/CN=Disposable integration CA", "-keyout", str(tls / "ca.key"),
                  "-out", str(tls / "ca.crt")], label="generate test CA")
        for name, cn, usage in (("server", "localhost", "serverAuth"),
                                ("client", "integration-client", "clientAuth"),
                                ("admin", "root", "clientAuth")):
            self.run(["openssl", "req", "-newkey", "rsa:2048", "-nodes", "-subj", "/CN=" + cn,
                      "-keyout", str(tls / (name + ".key")), "-out", str(tls / (name + ".csr"))],
                     label="generate disposable certificate request")
            extension = tls / (name + ".ext")
            extension.write_text("extendedKeyUsage=" + usage + "\nsubjectAltName=DNS:localhost,IP:127.0.0.1\n")
            self.run(["openssl", "x509", "-req", "-days", "1", "-in", str(tls / (name + ".csr")),
                      "-CA", str(tls / "ca.crt"), "-CAkey", str(tls / "ca.key"), "-CAcreateserial",
                      "-extfile", str(extension), "-out", str(tls / (name + ".crt"))],
                     label="sign disposable certificate")
        self.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1",
                  "-subj", "/CN=Untrusted disposable CA", "-keyout", str(tls / "wrong-ca.key"),
                  "-out", str(tls / "wrong-ca.crt")], label="generate wrong test CA")
        for key in tls.glob("*.key"):
            key.chmod(0o600)
            self.sensitive.extend(line for line in key.read_text().splitlines()
                                  if len(line) > 32 and not line.startswith("-----"))
        (tls / "password").write_text(self.password + "\n")
        (tls / "wrong-password").write_text(self.wrong_password + "\n")
        (tls / "password").chmod(0o600)
        (tls / "wrong-password").chmod(0o600)

    def restore(self, snapshot, label):
        directory = "/work/restore-" + uuid.uuid4().hex
        name = "ebk-restored-" + uuid.uuid4().hex
        self.exec(UTL, "snapshot", "restore", snapshot, "--data-dir=" + directory,
                  "--name=fixture", "--initial-cluster=fixture=http://127.0.0.1:32380",
                  "--initial-advertise-peer-urls=http://127.0.0.1:32380",
                  "--initial-cluster-token=fixture-32380", label="official snapshot restore")
        self.start(name, directory, 32379, 32380)
        self.check_keys(container=name, port=32379)
        self.passed(label)
        self.stop(name)

    def check_keys(self, *, tls=False, container=None, port=None):
        for key, value in (("integration/alpha", "fixture-alpha"), ("integration/beta", "fixture-beta")):
            result = self.ctl("get", key, "--print-value-only", tls=tls, container=container,
                              port=port, label="read restored fixture key")
            require(result.stdout.strip() == value, "restored fixture key/value mismatch")

    def fixtures(self):
        self.certificates()
        self.start(self.source, "/work/source", 2379, 2380, tls=True)
        self.run(["podman", "exec", "--detach", self.source, ETCD, "--name=plain", "--data-dir=/work/plain",
                  "--listen-client-urls=http://127.0.0.1:12379",
                  "--advertise-client-urls=http://127.0.0.1:12379",
                  "--listen-peer-urls=http://127.0.0.1:12380",
                  "--initial-advertise-peer-urls=http://127.0.0.1:12380",
                  "--initial-cluster=plain=http://127.0.0.1:12380",
                  "--initial-cluster-token=distinct-fixture", "--log-level=error"], label="start distinct cluster")
        self.ready()
        for tls in (False, True):
            for key, value in (("integration/alpha", "fixture-alpha"), ("integration/beta", "fixture-beta")):
                self.ctl("put", key, value, tls=tls, label="write disposable fixture key")
        self.ctl("snapshot", "save", "/work/official.db", label="official fixture snapshot")
        self.exec(UTL, "snapshot", "status", "/work/official.db", label="official snapshot status")
        self.restore("/work/official.db", "official snapshot/restore")
        self.ctl("user", "add", "root", "--no-password", tls=True, label="create fixture certificate admin")
        self.ctl("user", "grant-role", "root", "root", tls=True, label="grant fixture admin role")
        self.ctl("user", "add", "backup-test", "--interactive=false", tls=True,
                 input=self.password + "\n", label="create disposable password user via stdin")
        self.ctl("user", "grant-role", "backup-test", "root", tls=True, label="grant fixture backup role")
        self.ctl("auth", "enable", tls=True, label="enable fixture authentication")
        self.check_keys(tls=True)
        # Check password auth with a non-user client certificate as used by the
        # product. Only the private env-file PATH enters Podman's argv.
        auth_env = self.root / "tls" / "ctl-auth.env"
        auth_env.write_text("ETCDCTL_USER=backup-test\nETCDCTL_PASSWORD=" + self.password + "\n")
        auth_env.chmod(0o600)
        result = self.run(["podman", "exec", "--env-file", str(auth_env), self.source, CTL,
                           "--endpoints=https://127.0.0.1:2379", "--dial-timeout=2s", "--command-timeout=5s",
                           "--cacert=/work/tls/ca.crt", "--cert=/work/tls/client.crt",
                           "--key=/work/tls/client.key", "get", "integration/alpha", "--print-value-only"],
                          label="official password authentication via private env file")
        require(result.stdout.strip() == "fixture-alpha", "official authenticated fixture read mismatch")
        self.passed("TLS/mTLS/auth fixture")

    def config(self, *, endpoints=None, **overrides):
        values = dict(endpoints=endpoints or ["https://127.0.0.1:2379"], backup_dir="/work/backups", keep=2,
                      min_free_bytes=0, dial_timeout="2s", request_timeout="12s", ca_file="/work/tls/ca.crt",
                      cert_file="/work/tls/client.crt", key_file="/work/tls/client.key",
                      username="backup-test", password_file="/work/tls/password")
        values.update(overrides)
        # JSON strings/arrays/integers are valid TOML values for these scalar fields.
        (self.root / "config.toml").write_text("\n".join(k + " = " + json.dumps(v) for k, v in values.items()) + "\n")
        (self.root / "config.toml").chmod(0o600)

    def product(self, command, *extra, success=True):
        result = self.exec("/work/keeper", command, "--config", "/work/config.toml", "--json", *extra,
                           expected=None, timeout=40, label="product " + command)
        require((result.returncode == 0) == success, "product " + command + " unexpected success/failure")
        try:
            envelope = json.loads(result.stdout)
        except (ValueError, TypeError):
            raise Failure("product " + command + " did not emit one JSON envelope (output suppressed)") from None
        require(isinstance(envelope, dict) and envelope.get("ok") is success
                and envelope.get("command") == command, "product JSON envelope contract mismatch")
        if not success:
            require(bool(envelope.get("error")), "failure JSON envelope lacks error")
        return envelope

    def completed(self):
        root = self.root / "backups"
        if not root.exists():
            return {}
        result = {}
        for directory in root.iterdir():
            if directory.name.startswith("backup-"):
                require(directory.is_dir() and not directory.is_symlink(), "unsafe completed backup directory")
                files = {}
                for file in directory.iterdir():
                    require(file.is_file() and not file.is_symlink(), "unsafe completed backup child")
                    require(stat.S_IMODE(file.stat().st_mode) & 0o077 == 0, "backup file not private")
                    files[file.name] = hashlib.sha256(file.read_bytes()).hexdigest()
                require(stat.S_IMODE(directory.stat().st_mode) & 0o077 == 0, "backup directory not private")
                require({"snapshot.db", "metadata.json", "SHA256SUMS"} <= files.keys(), "completed backup missing files")
                result[directory.name] = files
        return result

    def failed_backup(self, name, **config):
        before = self.completed()
        self.config(**config)
        self.product("backup", success=False)
        require(self.completed() == before, name + " changed previous completed backups")
        self.passed(name)
        self.config()

    def product_restore(self, snapshot):
        directory = "/work/product-restore-" + uuid.uuid4().hex
        name = "ebk-restored-" + uuid.uuid4().hex
        self.exec(UTL, "snapshot", "restore", snapshot, "--data-dir=" + directory,
                  "--name=fixture", "--initial-cluster=fixture=http://127.0.0.1:32380",
                  "--initial-advertise-peer-urls=http://127.0.0.1:32380",
                  "--initial-cluster-token=fixture-32380", label="restore product with official tool")
        self.start(name, directory, 32379, 32380, tls=True)
        self.check_keys(tls=True, container=name, port=32379)
        self.passed("restore product snapshot")
        self.stop(name)

    def full(self):
        shutil.copyfile(self.args.binary, self.root / "keeper")
        (self.root / "keeper").chmod(0o700)
        self.binary_sha256 = hashlib.sha256((self.root / "keeper").read_bytes()).hexdigest()
        self.record("Binary SHA256: " + self.binary_sha256)
        self.exec("/work/keeper", "version", label="product version")
        self.passed("version")
        self.config()
        self.product("backup")
        first = self.completed()
        require(len(first) == 1, "initial backup must produce one completed directory")
        self.passed("backup")
        self.product("verify")
        first_name = next(iter(first))
        self.product("verify", "--backup", first_name)
        self.passed("verify all and named")
        self.product_restore("/work/backups/" + first_name + "/snapshot.db")
        self.config(endpoints=["https://127.0.0.1:1", "https://127.0.0.1:2379"])
        self.product("backup")
        require(len(self.completed()) == 2, "failover did not produce second completed backup")
        self.passed("endpoint failover")
        # Same namespace, separate TLS process, data, ports, and cluster identity.
        self.run(["podman", "exec", "--detach", self.source, ETCD,
                  "--name=distinct", "--data-dir=/work/distinct",
                  "--listen-client-urls=https://127.0.0.1:2479",
                  "--advertise-client-urls=https://127.0.0.1:2479",
                  "--listen-peer-urls=http://127.0.0.1:2480",
                  "--initial-advertise-peer-urls=http://127.0.0.1:2480",
                  "--initial-cluster=distinct=http://127.0.0.1:2480",
                  "--initial-cluster-token=distinct-tls-fixture", "--log-level=error",
                  "--cert-file=/work/tls/server.crt", "--key-file=/work/tls/server.key",
                  "--trusted-ca-file=/work/tls/ca.crt", "--client-cert-auth=true"],
                 label="start distinct TLS cluster")
        self.ready(tls=True, port=2479)
        self.config(backup_dir="/work/identity-backups", username="", password_file="",
                    cert_file="/work/tls/admin.crt", key_file="/work/tls/admin.key")
        self.product("backup")
        identity_root = self.root / "identity-backups"
        identity_before = self.tree_hashes(identity_root)
        self.config(endpoints=["https://127.0.0.1:2379", "https://127.0.0.1:2479"],
                    backup_dir="/work/identity-backups", username="", password_file="",
                    cert_file="/work/tls/admin.crt", key_file="/work/tls/admin.key")
        self.product("backup", success=False)
        require(self.tree_hashes(identity_root) == identity_before, "cluster mismatch changed prior backup set")
        self.passed("cluster mismatch preservation")
        self.config()
        for _ in range(3):
            before = self.completed()
            self.product("backup")
            after = self.completed()
            require(len(after) == 2 and len(after.keys() - before.keys()) == 1,
                    "retention must keep exactly two with the new backup preserved")
            self.product("verify")
        self.passed("retention keep=2")
        chosen = sorted(self.completed())[-1]
        snapshot = self.root / "backups" / chosen / "snapshot.db"
        original = snapshot.read_bytes()
        require(len(original) > 100, "snapshot unexpectedly small")
        mutant = bytearray(original)
        mutant[len(mutant) // 2] ^= 1
        try:
            snapshot.write_bytes(mutant)
            self.product("status")  # Listing deliberately does not hash file contents.
            self.product("verify", "--backup", chosen, success=False)
            self.product("verify", success=False)
        finally:
            snapshot.write_bytes(original)
        self.product("verify")
        self.passed("same-size tamper")
        for filename in ("metadata.json", "SHA256SUMS"):
            path = snapshot.parent / filename
            saved = path.read_bytes()
            try:
                path.write_bytes(b"invalid integration fixture\n")
                self.product("verify", "--backup", chosen, success=False)
            finally:
                path.write_bytes(saved)
        self.product("verify")
        self.passed("metadata/manifest corruption")
        self.failed_backup("space failure preservation", min_free_bytes=9223372036854775807)
        self.failed_backup("unreachable preservation", endpoints=["https://127.0.0.1:1"])
        self.failed_backup("wrong CA preservation", ca_file="/work/tls/wrong-ca.crt")
        self.failed_backup("wrong auth preservation", password_file="/work/tls/wrong-password")
        self.cancellation()
        # Stop every remote instance before running local commands in fresh containers.
        self.stop(self.source)
        shutil.rmtree(self.root / "tls")
        self.offline("status")
        self.offline("verify")
        self.offline("verify", "--backup", chosen)
        self.passed("offline status/verify")
        self.config(backup_dir="/work/empty-backups")
        (self.root / "empty-backups").mkdir(mode=0o700)
        self.offline("verify", success=False)
        self.passed("empty verify rejection")

    def cancellation(self):
        # Hold the real CLI in a long connection probe, then observe its actual
        # storage lock before signalling PID 1. This avoids timing a tiny snapshot.
        before = self.completed()
        self.config(endpoints=["http://127.0.0.1:1"], ca_file="", cert_file="", key_file="",
                    username="", password_file="", dial_timeout="15s", request_timeout="30s")
        name = "ebk-cancel-" + uuid.uuid4().hex
        self.containers.append(name)
        try:
            self.run(["podman", "run", "--detach", "--pull=never", "--network=none", "--name", name,
                      "--volume", str(self.root) + ":/work:z", "--entrypoint", "/work/keeper", self.args.image,
                      "backup", "--config", "/work/config.toml", "--json"], label="start cancellable CLI")
            lock = os.open(self.root / "backups" / ".lock", os.O_RDONLY | os.O_NOFOLLOW)
            try:
                deadline = time.monotonic() + 5
                while time.monotonic() < deadline:
                    try:
                        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
                        fcntl.flock(lock, fcntl.LOCK_UN)
                    except BlockingIOError:
                        break
                    time.sleep(0.02)
                else:
                    raise Failure("cancel fixture did not observe the active CLI storage lock")
            finally:
                os.close(lock)
            self.run(["podman", "kill", "--signal=TERM", name], label="cancel real CLI via SIGTERM")
            exited = self.run(["podman", "wait", name], timeout=10, label="wait for cancelled CLI")
            require(exited.stdout.strip() == "130", "cancelled CLI did not use cancellation exit status")
            result = self.run(["podman", "logs", name], label="cancelled CLI safe output")
            try:
                envelope = json.loads(result.stdout)
            except ValueError:
                raise Failure("cancelled CLI did not return JSON") from None
            require(envelope.get("ok") is False and envelope.get("error", {}).get("code") == "canceled",
                    "cancelled CLI did not report cancellation")
            require(self.completed() == before, "cancellation changed previous completed backups")
            self.stop(name)
            self.passed("cancellation preservation")
        finally:
            self.config()

    def offline(self, command, *extra, success=True):
        name = "ebk-offline-check-" + uuid.uuid4().hex
        self.containers.append(name)
        result = self.run(["podman", "run", "--pull=never", "--network=none", "--name", name,
                           "--volume", str(self.root) + ":/work:z", "--entrypoint", "/work/keeper", self.args.image,
                           command, "--config", "/work/config.toml", "--json", *extra],
                          timeout=40, expected=None, label="offline product " + command)
        require((result.returncode == 0) == success, "offline command unexpected exit status")
        try:
            envelope = json.loads(result.stdout)
        except ValueError:
            raise Failure("offline command invalid JSON") from None
        require(isinstance(envelope, dict) and envelope.get("ok") is success
                and envelope.get("command") == command, "offline command envelope mismatch")
        if not success:
            require(bool(envelope.get("error")), "offline failure lacks error")
        self.stop(name)

    @staticmethod
    def tree_hashes(root):
        return {str(p.relative_to(root)): hashlib.sha256(p.read_bytes()).hexdigest()
                for p in root.rglob("*") if p.is_file() and p.name != ".lock"}

    def stop(self, name):
        self.run(["podman", "rm", "--force", "--ignore", name], timeout=20,
                 label="remove exact owned container")
        self.containers.remove(name)

    def cleanup(self):
        failed = False
        for name in list(reversed(self.containers)):
            try:
                self.stop(name)
            except Failure:
                failed = True
                self.record("FAIL cleanup exact-owned container: " + name)
        if self.containers:
            # Never delete mounted data under an etcd process we could not stop.
            self.record("FAIL fixture cleanup deferred until owned containers stop: " + str(self.root))
        else:
            try:
                shutil.rmtree(self.root)
            except OSError:
                failed = True
                self.record("FAIL cleanup private fixture directory: " + str(self.root))
        return not failed

    def execute(self):
        success = False
        try:
            info = self.run(["podman", "info", "--format", "{{.Host.Security.Rootless}}"], label="rootless preflight")
            require(info.stdout.strip() == "true", "rootless Podman is required")
            image = self.run(["podman", "image", "inspect", self.args.image, "--format", "{{.Id}}"], label="cached image preflight")
            self.image_id = image.stdout.strip()
            self.record("Cached image ID: " + self.image_id)
            self.fixtures()
            if not self.args.fixture_only:
                self.full()
            success = True
        except (Failure, KeyboardInterrupt) as exc:
            self.record("FAIL " + (str(exc) if isinstance(exc, Failure) else "interrupted"))
        except Exception as exc:
            # Do not expose raw exceptions that might contain captured output/secrets.
            self.record("FAIL unexpected harness exception: " + type(exc).__name__)
        finally:
            cleaned = self.cleanup()
            for name in SCENARIOS:
                if name not in self.results:
                    self.record("NOT RUN " + name)
            self.record("NOT RUN deterministic mid-stream network-failure injection (covered by engine unit tests only)")
            result = ("PASS fixture provisioning only; product acceptance NOT RUN"
                      if success and cleaned and self.args.fixture_only else
                      "PASS full implemented scenario set" if success and cleaned else "FAIL")
            self.record("RESULT " + result)
            summary = {
                "schema_version": 1,
                "mode": "fixtures_only" if self.args.fixture_only else "full",
                "result": result,
                "success": success and cleaned,
                "cleanup_success": cleaned,
                "image": self.args.image,
                "image_id": self.image_id,
                "binary_sha256": self.binary_sha256,
                "scenarios": {name: self.results.get(name, "NOT RUN") for name in SCENARIOS},
                "not_implemented": ["deterministic mid-stream network-failure integration injection"],
            }
            with (self.evidence / "summary.json").open("x", encoding="utf-8") as output:
                json.dump(summary, output, indent=2)
                output.write("\n")
            self.log.close()
        return 0 if success and cleaned else 1


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", default=DEFAULT_IMAGE,
                        help="pre-cached etcd image for every scenario (default: %(default)s)")
    parser.add_argument("--binary", type=Path, help="explicit prebuilt STATIC Linux CLI path; never builds Go")
    parser.add_argument("--fixtures-only", "--fixture-only", dest="fixture_only", action="store_true",
                        help="validate official etcd fixtures only, NOT the product")
    parser.add_argument("--evidence-dir", type=Path, help="new/private output directory outside the checkout")
    args = parser.parse_args()
    if not args.fixture_only and args.binary is None:
        parser.error("--binary is required unless --fixtures-only is selected")
    if args.binary is not None:
        args.binary = args.binary.resolve(strict=True)
        static_elf(args.binary)
    checkout = Path(__file__).resolve().parents[1]
    if args.evidence_dir is not None and args.evidence_dir.exists():
        parser.error("--evidence-dir must name a new directory")
    args.evidence_dir = (args.evidence_dir or Path(tempfile.mkdtemp(prefix="etcd-keeper-integration-evidence-"))).resolve()
    if args.evidence_dir == checkout or checkout in args.evidence_dir.parents:
        parser.error("evidence must be outside the checkout")
    for executable in ("podman", "openssl"):
        if shutil.which(executable) is None:
            parser.error(executable + " is required (no installation is attempted)")
    def interrupted(signum, frame):
        raise KeyboardInterrupt
    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)
    harness = Harness(args)
    return harness.execute()


if __name__ == "__main__":
    sys.exit(main())
