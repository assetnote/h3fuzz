#!/usr/bin/env python3
"""PERF-003 evidence collector used by the shell entry points.

The collectors are deliberately read-only. Fixture roots replace /proc, /sys,
and /etc for deterministic tests; production collection never writes there.
"""

from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import socket
import subprocess
import sys
import tempfile
from typing import Any
from urllib.parse import urlsplit

PROVIDER_VERSION = "perf003-snapshot-v1"
QUIC_GO_BUFFER_TARGETS = {"v0.63.0": 7 << 20}
SENSITIVE = re.compile(
    r"(?i)(authorization|cookie|credential|password|passwd|secret|token|api[-_]?key|private[-_]?key)"
)
HEX64 = re.compile(r"^[0-9a-f]{64}$")


class SnapshotError(RuntimeError):
    pass


class HostView:
    def __init__(self, fixture_root: Path | None):
        self.root = fixture_root
        self.meta: dict[str, Any] = {}
        if fixture_root is not None:
            metadata = fixture_root / "fixture.json"
            if not metadata.is_file():
                raise SnapshotError(f"fixture metadata not found: {metadata}")
            self.meta = json.loads(metadata.read_text(encoding="utf-8"))

    def path(self, absolute: str) -> Path:
        if not absolute.startswith("/"):
            raise SnapshotError(f"host path must be absolute: {absolute}")
        if self.root is None:
            return Path(absolute)
        return self.root / absolute.removeprefix("/")

    def read(self, absolute: str, required: bool = True) -> str | None:
        path = self.path(absolute)
        try:
            return path.read_text(encoding="utf-8").strip()
        except (OSError, UnicodeDecodeError):
            if required:
                raise SnapshotError(f"cannot read required host evidence: {absolute}")
            return None

    def uname(self) -> dict[str, str]:
        if self.root is not None:
            value = self.meta["uname"]
            return {
                "sysname": str(value["sysname"]),
                "release": str(value["release"]),
                "machine": str(value["machine"]),
            }
        value = platform.uname()
        return {
            "sysname": value.system,
            "release": value.release,
            "machine": value.machine,
        }

    def captured_at(self) -> str:
        if self.root is not None:
            return str(self.meta["captured_at"])
        now_ns = __import__("time").time_ns()
        seconds, nanos = divmod(now_ns, 1_000_000_000)
        base = dt.datetime.fromtimestamp(seconds, tz=dt.timezone.utc).strftime(
            "%Y-%m-%dT%H:%M:%S"
        )
        return f"{base}.{nanos:09d}Z"

    def hostname(self) -> str:
        if self.root is not None:
            return str(self.meta["hostname"])
        return socket.gethostname()

    def namespace_inode(self) -> str:
        if self.root is not None and "network_namespace_inode" in self.meta:
            return str(self.meta["network_namespace_inode"])
        path = self.path("/proc/self/ns/net")
        try:
            target = os.readlink(path)
        except OSError:
            target = self.read("/proc/self/ns/net") or ""
        match = re.search(r"\[([0-9]+)\]", target)
        if not match:
            raise SnapshotError("cannot determine network namespace inode")
        return match.group(1)


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def hash_canonical_files(root: Path, excluded: set[Path]) -> dict[str, Any]:
    root = root.resolve()
    git_available = False
    git_commit: str | None = None
    dirty: bool | None = None
    files: list[str]
    try:
        top = subprocess.run(
            ["git", "-C", str(root), "rev-parse", "--show-toplevel"],
            check=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            text=True,
        ).stdout.strip()
        if Path(top).resolve() != root:
            raise SnapshotError("--source-root must be the Git worktree root")
        raw = subprocess.run(
            ["git", "-C", str(root), "ls-files", "-co", "--exclude-standard", "-z"],
            check=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        ).stdout
        files = sorted(
            {
                part.decode("utf-8", "surrogateescape")
                for part in raw.split(b"\0")
                if part
            }
        )
        git_commit = subprocess.run(
            ["git", "-C", str(root), "rev-parse", "HEAD"],
            check=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            text=True,
        ).stdout.strip()
        dirty = bool(
            subprocess.run(
                [
                    "git",
                    "-C",
                    str(root),
                    "status",
                    "--porcelain=v1",
                    "--untracked-files=all",
                ],
                check=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.DEVNULL,
            ).stdout
        )
        git_available = True
        algorithm = "git-ls-files-worktree-v1"
    except (FileNotFoundError, subprocess.CalledProcessError, SnapshotError):
        files = sorted(
            str(path.relative_to(root))
            for path in root.rglob("*")
            if (path.is_file() or path.is_symlink())
            and ".git" not in path.relative_to(root).parts
            and "harness/.work" not in str(path.relative_to(root))
        )
        algorithm = "filesystem-worktree-v1"

    digest = hashlib.sha256()
    count = 0
    excluded_resolved = {path.resolve() for path in excluded}
    for relative in files:
        path = root / relative
        try:
            if path.resolve() in excluded_resolved:
                continue
        except OSError:
            pass
        encoded = relative.encode("utf-8", "surrogateescape")
        if path.is_symlink():
            kind = b"symlink"
            content = os.readlink(path).encode("utf-8", "surrogateescape")
            mode = path.lstat().st_mode & 0o7777
        elif path.is_file():
            kind = b"file"
            content = path.read_bytes()
            mode = path.stat().st_mode & 0o7777
        else:
            kind = b"missing"
            content = b""
            mode = 0
        digest.update(
            kind
            + b"\0"
            + format(mode, "04o").encode()
            + b"\0"
            + str(len(encoded)).encode()
            + b"\0"
            + encoded
        )
        digest.update(b"\0" + str(len(content)).encode() + b"\0" + content + b"\0")
        count += 1
    return {
        "canonical_sha256": digest.hexdigest(),
        "algorithm": algorithm,
        "file_count": count,
        "git_commit": git_commit,
        "git_available": git_available,
        "dirty": dirty,
    }


def parse_os_release(host: HostView) -> dict[str, str]:
    text = host.read("/etc/os-release", required=False) or ""
    result: dict[str, str] = {}
    for line in text.splitlines():
        if "=" not in line or line.lstrip().startswith("#"):
            continue
        key, value = line.split("=", 1)
        result[key.lower()] = value.strip().strip('"').strip("'")
    return result


def cpu_model(host: HostView) -> str:
    if host.root is not None and "cpu_model" in host.meta:
        return str(host.meta["cpu_model"])
    text = host.read("/proc/cpuinfo", required=False) or ""
    for name in ("model name", "hardware", "processor"):
        match = re.search(rf"(?m)^{re.escape(name)}\s*:\s*(.+)$", text)
        if match and not match.group(1).strip().isdigit():
            return match.group(1).strip()
    return platform.processor() or "unknown"


def logical_cpu_count(host: HostView) -> int:
    if host.root is not None and "logical_cpus" in host.meta:
        return int(host.meta["logical_cpus"])
    text = host.read("/proc/cpuinfo", required=False) or ""
    count = len(re.findall(r"(?m)^processor\s*:", text))
    return count or (os.cpu_count() or 1)


def parse_cpu_list(value: str) -> list[int]:
    cpus: set[int] = set()
    for item in value.strip().split(","):
        if not item:
            continue
        if "-" in item:
            start, end = item.split("-", 1)
            cpus.update(range(int(start), int(end) + 1))
        else:
            cpus.add(int(item))
    return sorted(cpus)


def current_affinity(host: HostView, logical_cpus: int) -> list[int]:
    if host.root is not None and "affinity_cpus" in host.meta:
        return sorted(int(value) for value in host.meta["affinity_cpus"])
    if hasattr(os, "sched_getaffinity"):
        return sorted(os.sched_getaffinity(0))
    return list(range(logical_cpus))


def process_affinity(host: HostView, pid: int) -> list[int]:
    status = host.read(f"/proc/{pid}/status", required=False) or ""
    match = re.search(r"(?m)^Cpus_allowed_list:\s*(.+)$", status)
    if not match:
        raise SnapshotError(f"cannot determine CPU affinity for PID {pid}")
    return parse_cpu_list(match.group(1))


def frequency_governor(host: HostView) -> str:
    if host.root is not None and "frequency_governor" in host.meta:
        return str(host.meta["frequency_governor"])
    base = host.path("/sys/devices/system/cpu")
    governors: set[str] = set()
    try:
        for path in base.glob("cpu[0-9]*/cpufreq/scaling_governor"):
            value = path.read_text(encoding="utf-8").strip()
            if value:
                governors.add(value)
    except OSError:
        pass
    return ",".join(sorted(governors)) or "unavailable"


def memory_bytes(host: HostView) -> int:
    if host.root is not None and "memory_bytes" in host.meta:
        return int(host.meta["memory_bytes"])
    text = host.read("/proc/meminfo", required=False) or ""
    match = re.search(r"(?m)^MemTotal:\s*([0-9]+)\s+kB$", text)
    if match:
        return int(match.group(1)) * 1024
    if host.root is None:
        try:
            return int(
                subprocess.check_output(
                    ["sysctl", "-n", "hw.memsize"], text=True
                ).strip()
            )
        except (OSError, subprocess.CalledProcessError, ValueError):
            pass
    raise SnapshotError("cannot determine total memory")


def udp_sysctls(host: HostView) -> dict[str, int]:
    values: dict[str, int] = {}
    for name in ("rmem_default", "rmem_max", "wmem_default", "wmem_max"):
        text = host.read(f"/proc/sys/net/core/{name}", required=False)
        if text is None or not text.isdigit():
            raise SnapshotError(f"cannot read net.core.{name}")
        values[name] = int(text)
    return values


def boot_id(host: HostView) -> str:
    value = host.read("/proc/sys/kernel/random/boot_id", required=False)
    if not value:
        raise SnapshotError("cannot determine boot ID")
    return value.lower()


def module_versions(
    binary: Path, source_root: Path, host: HostView
) -> tuple[str, dict[str, str]]:
    if host.root is not None:
        return str(host.meta["go_version"]), {
            str(key): str(value)
            for key, value in host.meta.get("go_modules", {}).items()
        }
    go_version = "unavailable"
    modules: dict[str, str] = {}
    try:
        output = subprocess.run(
            ["go", "version", "-m", str(binary)],
            check=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            text=True,
        ).stdout
        lines = output.splitlines()
        if lines:
            first = lines[0].split()
            if len(first) >= 2:
                go_version = first[1]
        for line in output.splitlines():
            fields = line.strip().split("\t")
            if len(fields) >= 3 and fields[0] == "dep":
                modules[fields[1]] = fields[2]
    except (FileNotFoundError, subprocess.CalledProcessError):
        pass
    go_mod = source_root / "go.mod"
    if go_mod.is_file():
        text = go_mod.read_text(encoding="utf-8")
        if go_version == "unavailable":
            match = re.search(r"(?m)^go\s+([^\s]+)", text)
            if match:
                go_version = "go" + match.group(1)
        for module in ("github.com/quic-go/quic-go", "github.com/quic-go/qpack"):
            match = re.search(rf"(?m)^\s*{re.escape(module)}\s+([^\s]+)", text)
            if match:
                modules.setdefault(module, match.group(1))
    return go_version, modules


def quic_buffer_target(version: str) -> tuple[int | None, str]:
    if version in QUIC_GO_BUFFER_TARGETS:
        return QUIC_GO_BUFFER_TARGETS[version], "pinned-version-map"
    return None, "unsupported-quic-go-version"


def container_runtime(host: HostView) -> dict[str, str | None]:
    if host.root is not None:
        value = host.meta.get("container_runtime", {})
        return {"name": value.get("name"), "version": value.get("version")}
    for name, command in (
        ("docker", ["docker", "--version"]),
        ("podman", ["podman", "--version"]),
    ):
        try:
            value = subprocess.run(
                command,
                check=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.DEVNULL,
                text=True,
            ).stdout.strip()
            return {"name": name, "version": value}
        except (FileNotFoundError, subprocess.CalledProcessError):
            continue
    return {"name": None, "version": None}


def image_identity(reference: str | None, host: HostView) -> dict[str, Any]:
    empty = {
        "reference": reference,
        "image_id_or_digest": None,
        "config_sha256": None,
        "registry_digest": None,
    }
    if not reference:
        return empty
    if host.root is not None:
        value = host.meta.get("images", {}).get(reference)
        if not value:
            return empty
        return {"reference": reference, **value}
    try:
        output = subprocess.run(
            ["docker", "image", "inspect", reference],
            check=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            text=True,
        ).stdout
        record = json.loads(output)[0]
        config = json.dumps(
            record.get("Config", {}), sort_keys=True, separators=(",", ":")
        ).encode()
        digests = record.get("RepoDigests") or []
        return {
            "reference": reference,
            "image_id_or_digest": record.get("Id"),
            "config_sha256": hashlib.sha256(config).hexdigest(),
            "registry_digest": digests[0] if digests else None,
        }
    except (
        FileNotFoundError,
        subprocess.CalledProcessError,
        KeyError,
        IndexError,
        json.JSONDecodeError,
    ):
        return empty


def sanitize_environment(value: Any) -> dict[str, Any]:
    if not isinstance(value, dict):
        raise SnapshotError("environment JSON must be an object")
    return {
        str(key): "<redacted>" if SENSITIVE.search(str(key)) else item
        for key, item in sorted(value.items())
    }


def sanitize_argv(value: Any) -> list[str]:
    if not isinstance(value, list) or not all(isinstance(item, str) for item in value):
        raise SnapshotError("command JSON must be an array of strings")
    result: list[str] = []
    redact_next = False
    for item in value:
        if redact_next:
            result.append("<redacted>")
            redact_next = False
            continue
        if item.startswith("-") and "=" in item:
            name, _ = item.split("=", 1)
            result.append(name + "=<redacted>" if SENSITIVE.search(name) else item)
            continue
        result.append(item)
        if item.startswith("-") and SENSITIVE.search(item):
            redact_next = True
    return result


def ping_rtt_ms(hostname: str) -> float | None:
    try:
        output = subprocess.run(
            ["ping", "-n", "-c", "3", hostname],
            check=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            text=True,
            timeout=5,
        ).stdout
    except (
        FileNotFoundError,
        subprocess.CalledProcessError,
        subprocess.TimeoutExpired,
    ):
        return None
    match = re.search(r"(?:rtt|round-trip)[^=]*=\s*[^/]+/([^/]+)/", output)
    if not match:
        return None
    try:
        return float(match.group(1))
    except ValueError:
        return None


def target_evidence(
    target: str | None, host: HostView, explicit_rtt_ms: float | None
) -> dict[str, Any] | None:
    if not target:
        return None
    parsed = urlsplit(target)
    if not parsed.hostname:
        raise SnapshotError("--target must contain a hostname")
    if explicit_rtt_ms is not None:
        if host.root is not None:
            addresses = host.meta.get("dns", {}).get(parsed.hostname, [])
        else:
            try:
                addresses = sorted(
                    {
                        item[4][0]
                        for item in socket.getaddrinfo(
                            parsed.hostname, parsed.port or 443
                        )
                    }
                )
            except socket.gaierror:
                addresses = []
        rtt = explicit_rtt_ms
        rtt_source = "operator_measurement"
    elif host.root is not None:
        addresses = host.meta.get("dns", {}).get(parsed.hostname, [])
        rtt = host.meta.get("rtt_ms", {}).get(parsed.hostname)
        rtt_source = "fixture_measurement" if rtt is not None else None
    else:
        try:
            addresses = sorted(
                {
                    item[4][0]
                    for item in socket.getaddrinfo(parsed.hostname, parsed.port or 443)
                }
            )
        except socket.gaierror:
            addresses = []
        rtt = ping_rtt_ms(parsed.hostname)
        rtt_source = "icmp_ping_average" if rtt is not None else None
    return {
        "url": target,
        "hostname": parsed.hostname,
        "resolved_addresses": addresses,
        "measured_rtt_ms": rtt,
        "rtt_source": rtt_source,
    }


def wordlist_evidence(
    path_value: str | None, repeat_mode: str | None
) -> dict[str, Any] | None:
    if not path_value:
        return None
    path = Path(path_value).resolve()
    data = path.read_bytes()
    return {
        "path": str(path),
        "line_count": len(data.splitlines()),
        "sha256": hashlib.sha256(data).hexdigest(),
        "repeat_mode": repeat_mode,
    }


def quic_go_log_evidence(path_value: str | None) -> dict[str, Any]:
    if not path_value:
        return {
            "path": None,
            "retained": False,
            "sha256": None,
            "size_bytes": None,
            "udp_buffer_warning_count": None,
            "udp_buffer_warning_line_sha256": [],
            "unresolved_udp_buffer_warning": None,
        }
    path = Path(path_value).resolve()
    data = path.read_bytes()
    text = data.decode("utf-8", "replace")
    warning_pattern = re.compile(
        r"(?i)(?:failed to (?:sufficiently )?increase.*(?:receive|send).*buffer|"
        r"(?:receive|send).*buffer.*(?:too small|increase failed))"
    )
    warnings = [line for line in text.splitlines() if warning_pattern.search(line)]
    return {
        "path": str(path),
        "retained": True,
        "sha256": hashlib.sha256(data).hexdigest(),
        "size_bytes": len(data),
        "udp_buffer_warning_count": len(warnings),
        "udp_buffer_warning_line_sha256": [
            hashlib.sha256(line.encode("utf-8")).hexdigest() for line in warnings
        ],
        "unresolved_udp_buffer_warning": bool(warnings),
    }


def host_identity(
    host: HostView, kernel: dict[str, str], model: str, logical_cpus: int
) -> str:
    machine_id = host.read("/etc/machine-id", required=False) or ""
    material = {
        "machine_id": machine_id,
        "hostname": host.hostname(),
        "machine": kernel["machine"],
        "cpu_model": model,
        "logical_cpus": logical_cpus,
    }
    encoded = json.dumps(material, sort_keys=True, separators=(",", ":")).encode()
    return hashlib.sha256(encoded).hexdigest()


def parse_process_spec(value: str) -> tuple[str, int]:
    try:
        role, raw_pid = value.split(":", 1)
        pid = int(raw_pid)
    except (ValueError, TypeError) as error:
        raise argparse.ArgumentTypeError("process must be ROLE:PID") from error
    if not role or pid < 1:
        raise argparse.ArgumentTypeError("process must be ROLE:PID with a positive PID")
    return role, pid


def parse_socket_identity(value: str) -> tuple[str, str, str | int]:
    try:
        inode, identity = value.split("=", 1)
        kind, raw_value = identity.split(":", 1)
    except ValueError as error:
        raise argparse.ArgumentTypeError(
            "socket identity must be INODE=slot:N or INODE=shared:IDENTITY"
        ) from error
    if not inode.isdigit() or int(inode) < 1:
        raise argparse.ArgumentTypeError("socket identity inode must be positive")
    if kind == "slot":
        try:
            parsed: str | int = int(raw_value)
        except ValueError as error:
            raise argparse.ArgumentTypeError(
                "slot identity must be an integer"
            ) from error
        if parsed < 0:
            raise argparse.ArgumentTypeError("slot identity must be non-negative")
    elif kind == "shared" and raw_value:
        parsed = raw_value
    else:
        raise argparse.ArgumentTypeError(
            "socket identity must be INODE=slot:N or INODE=shared:IDENTITY"
        )
    return inode, kind, parsed


def udp_counters(host: HostView) -> list[dict[str, Any]] | None:
    text = host.read("/proc/net/snmp", required=False)
    if not text:
        return None
    lines = text.splitlines()
    for index, line in enumerate(lines[:-1]):
        if not line.startswith("Udp:") or not lines[index + 1].startswith("Udp:"):
            continue
        names = line.split()[1:]
        values = lines[index + 1].split()[1:]
        if len(names) != len(values) or any(not value.isdigit() for value in values):
            return None
        return [
            {
                "source": "/proc/net/snmp:Udp",
                "name": name,
                "value": value,
                "width_bits": 64,
            }
            for name, value in zip(names, values)
        ]
    return None


def interface_counters(host: HostView) -> list[dict[str, Any]] | None:
    base = host.path("/sys/class/net")
    results: list[dict[str, Any]] = []
    try:
        interfaces = sorted(path for path in base.iterdir() if path.is_dir())
    except OSError:
        return None
    for interface in interfaces:
        try:
            ifindex = int((interface / "ifindex").read_text(encoding="utf-8").strip())
            stats = {
                name: (interface / "statistics" / name)
                .read_text(encoding="utf-8")
                .strip()
                for name in (
                    "rx_bytes",
                    "tx_bytes",
                    "rx_dropped",
                    "tx_dropped",
                    "rx_errors",
                    "tx_errors",
                )
            }
            if any(not value.isdigit() for value in stats.values()):
                continue
            speed_path = interface / "speed"
            speed: str | None = None
            try:
                speed_mbps = int(speed_path.read_text(encoding="utf-8").strip())
                if speed_mbps >= 0:
                    speed = str(speed_mbps * 1_000_000)
            except (OSError, ValueError):
                pass
            results.append(
                {
                    "ifindex": ifindex,
                    "name": interface.name,
                    "link_speed_bits_per_second": speed,
                    **stats,
                    "counter_width_bits": 64,
                    "source": "sysfs",
                }
            )
        except (OSError, ValueError):
            continue
    return results or None


def clock_ticks(host: HostView) -> int:
    if host.root is not None:
        return int(host.meta.get("clock_ticks", 100))
    try:
        return int(os.sysconf("SC_CLK_TCK"))
    except (OSError, ValueError):
        return 100


def cgroup_paths(text: str) -> list[str]:
    paths: list[str] = []
    for line in text.splitlines():
        fields = line.split(":", 2)
        if len(fields) == 3:
            paths.append(fields[2])
    return sorted(set(paths))


def _find_throttle_counter(
    direct_directory: Path, base_directory: Path, field: str, multiplier: int
) -> tuple[int | None, Path | None, bool]:
    directory = direct_directory
    while directory == base_directory or base_directory in directory.parents:
        try:
            stats = dict(
                item.split(None, 1)
                for item in (directory / "cpu.stat")
                .read_text(encoding="utf-8")
                .splitlines()
                if len(item.split(None, 1)) == 2
            )
            if field in stats:
                return (
                    int(stats[field]) * multiplier,
                    directory,
                    directory == direct_directory,
                )
        except (OSError, ValueError):
            pass
        if directory == base_directory:
            break
        directory = directory.parent
    return None, None, False


def cgroup_throttled_ns(
    host: HostView, cgroup_text: str
) -> tuple[int | None, str | None, bool]:
    lines = cgroup_text.splitlines()
    for line in lines:
        fields = line.split(":", 2)
        if len(fields) != 3:
            continue
        hierarchy, controllers, relative = fields
        if hierarchy == "0" and controllers == "":
            base = host.path("/sys/fs/cgroup")
            direct = base / relative.lstrip("/")
            value, source, exact = _find_throttle_counter(
                direct, base, "throttled_usec", 1000
            )
            return value, str(source) if source is not None else None, exact
        if "cpu" in controllers.split(","):
            base = host.path(f"/sys/fs/cgroup/{controllers}")
            direct = base / relative.lstrip("/")
            value, source, exact = _find_throttle_counter(
                direct, base, "throttled_time", 1
            )
            return value, str(source) if source is not None else None, exact
    return None, None, False


def process_evidence(host: HostView, role: str, pid: int) -> dict[str, Any] | None:
    stat_text = host.read(f"/proc/{pid}/stat", required=False)
    cgroup_text = host.read(f"/proc/{pid}/cgroup", required=False)
    if not stat_text or cgroup_text is None:
        return None
    close = stat_text.rfind(")")
    if close < 0:
        return None
    fields = stat_text[close + 2 :].split()
    if len(fields) <= 19:
        return None
    try:
        user_ticks = int(fields[11])
        system_ticks = int(fields[12])
        start_ticks = fields[19]
        ticks = clock_ticks(host)
        throttled, throttle_source, throttle_exact = cgroup_throttled_ns(
            host, cgroup_text
        )
        if throttled is None or throttle_source is None:
            return None
        normalized_paths = "\n".join(cgroup_paths(cgroup_text)).encode()
        return {
            "role": role,
            "pid": pid,
            "start_ticks": start_ticks,
            "affinity_cpus": process_affinity(host, pid),
            "cpu_time_ns": str((user_ticks + system_ticks) * 1_000_000_000 // ticks),
            "cgroup_path_sha256": hashlib.sha256(normalized_paths).hexdigest(),
            "cgroup_cpu_throttled_ns": str(throttled),
            "cgroup_cpu_throttle_source_path_sha256": hashlib.sha256(
                throttle_source.encode()
            ).hexdigest(),
            "cgroup_cpu_throttle_scope": "process" if throttle_exact else "ancestor",
        }
    except (ValueError, SnapshotError):
        return None


def process_start_ticks(host: HostView, pid: int) -> str | None:
    stat_text = host.read(f"/proc/{pid}/stat", required=False)
    if not stat_text:
        return None
    close = stat_text.rfind(")")
    if close < 0:
        return None
    fields = stat_text[close + 2 :].split()
    if len(fields) <= 19 or not fields[19].isdigit():
        return None
    return fields[19]


def ss_output(host: HostView) -> str | None:
    if host.root is not None:
        return host.read("/ss.txt", required=False)
    try:
        return subprocess.run(
            ["ss", "-H", "-u", "-a", "-n", "-m", "-p", "-e"],
            check=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            text=True,
        ).stdout
    except (FileNotFoundError, subprocess.CalledProcessError):
        return None


def group_ss_records(text: str) -> list[str]:
    records: list[str] = []
    current = ""
    for line in text.splitlines():
        if not line.strip():
            continue
        if line[:1].isspace() and current:
            current += " " + line.strip()
        else:
            if current:
                records.append(current)
            current = line.strip()
    if current:
        records.append(current)
    return records


def socket_evidence(
    host: HostView, processes: list[tuple[str, int]], target_bytes: int
) -> list[dict[str, Any]] | None:
    text = ss_output(host)
    if not text:
        return None
    roles = {pid: role for role, pid in processes}
    results: list[dict[str, Any]] = []
    for record in group_ss_records(text):
        pid_match = re.search(r"\bpid=([0-9]+)", record)
        inode_match = re.search(r"\bino:([0-9]+)\b", record)
        receive_match = re.search(r"(?:^|,)rb([0-9]+)(?:,|\))", record)
        send_match = re.search(r"(?:^|,)tb([0-9]+)(?:,|\))", record)
        parts = record.split()
        if (
            not (pid_match and inode_match and receive_match and send_match)
            or len(parts) < 5
        ):
            continue
        pid = int(pid_match.group(1))
        if pid not in roles:
            continue
        receive = int(receive_match.group(1))
        send = int(send_match.group(1))
        results.append(
            {
                "role": roles[pid],
                "pid": pid,
                "inode": inode_match.group(1),
                "local_address": parts[3],
                "raw_so_rcvbuf_bytes": receive,
                "raw_so_sndbuf_bytes": send,
                "operator_requested_so_rcvbuf_bytes": target_bytes,
                "operator_requested_so_sndbuf_bytes": target_bytes,
                "source": "ss",
                "linux_kernel_doubling_applies": True,
                "required_minimum_bytes": target_bytes,
                "meets_receive_target": receive >= target_bytes,
                "meets_send_target": send >= target_bytes,
            }
        )
    return sorted(results, key=lambda item: (item["pid"], int(item["inode"]))) or None


def host_cpu_point(host: HostView) -> dict[str, Any] | None:
    text = host.read("/proc/stat", required=False) or ""
    line = next((item for item in text.splitlines() if item.startswith("cpu ")), None)
    if not line:
        return None
    names = (
        "user",
        "nice",
        "system",
        "idle",
        "iowait",
        "irq",
        "softirq",
        "steal",
        "guest",
        "guest_nice",
    )
    values = line.split()[1:]
    if any(not value.isdigit() for value in values):
        return None
    return {
        "source": "/proc/stat",
        "clock_ticks_per_second": clock_ticks(host),
        "ticks": {name: value for name, value in zip(names, values)},
    }


def point_snapshot(args: argparse.Namespace) -> int:
    host = HostView(Path(args.fixture_root).resolve() if args.fixture_root else None)
    kernel = host.uname()
    model = cpu_model(host)
    logical_cpus = logical_cpu_count(host)
    target_bytes, target_source = quic_buffer_target(args.quic_go_version)
    if target_bytes is None:
        raise SnapshotError(
            f"no buffer target is known for quic-go {args.quic_go_version}"
        )
    processes_requested: list[tuple[str, int]] = list(args.process or [])
    counters = udp_counters(host)
    interfaces = interface_counters(host)
    process_rows_raw = [
        process_evidence(host, role, pid) for role, pid in processes_requested
    ]
    process_rows = [row for row in process_rows_raw if row is not None] or None
    sockets = socket_evidence(host, processes_requested, target_bytes)
    try:
        captured_boot_id: str | None = boot_id(host)
    except SnapshotError:
        captured_boot_id = None
    try:
        namespace_inode: str | None = host.namespace_inode()
    except SnapshotError:
        namespace_inode = None
    reasons: list[str] = []
    if kernel["sysname"] != "Linux":
        reasons.append("native_linux_required")
    if counters is None:
        reasons.append("udp_counters_unavailable")
    if captured_boot_id is None:
        reasons.append("boot_id_unavailable")
    if namespace_inode is None:
        reasons.append("network_namespace_unavailable")
    if interfaces is None:
        reasons.append("interface_counters_unavailable")
    if not processes_requested:
        reasons.append("process_identity_required")
    elif process_rows is None or len(process_rows) != len(processes_requested):
        reasons.append("process_evidence_unavailable")
    elif any(row["cgroup_cpu_throttle_scope"] != "process" for row in process_rows):
        reasons.append("cgroup_cpu_throttling_ancestor_scoped")
    if sockets is None:
        reasons.append("udp_socket_evidence_unavailable")
    else:
        present = {item["pid"] for item in sockets}
        if any(pid not in present for _, pid in processes_requested):
            reasons.append("udp_socket_missing_for_process")
        if any(
            item["raw_so_rcvbuf_bytes"] < target_bytes
            or item["raw_so_sndbuf_bytes"] < target_bytes
            for item in sockets
        ):
            reasons.append("udp_socket_buffer_below_quic_go_target")
    snapshot: dict[str, Any] = {
        "schema_version": 1,
        "kind": args.kind,
        "provider_version": PROVIDER_VERSION,
        "phase": args.phase,
        "run_id": args.run_id.lower(),
        "captured_at": host.captured_at(),
        "eligible": not reasons,
        "ineligibility_reasons": sorted(set(reasons)),
        "host_identity_sha256": host_identity(host, kernel, model, logical_cpus),
        "boot_id": captured_boot_id,
        "network_namespace_inode": namespace_inode,
        "udp_counters": counters,
        "interfaces": interfaces,
        "processes": process_rows,
        "sockets": sockets,
        "host_cpu": host_cpu_point(host),
        "frequency_governor": frequency_governor(host),
        "quic_udp_buffer_target": {
            "bytes": target_bytes,
            "dependency": "github.com/quic-go/quic-go",
            "version": args.quic_go_version,
            "derivation": target_source,
        },
    }
    if args.kind == "server_point":
        schema_path = (
            Path(__file__).resolve().parent.parent
            / "schemas"
            / "server-point-v1.schema.json"
        )
        validate_schema(snapshot, json.loads(schema_path.read_text(encoding="utf-8")))
    atomic_json(Path(args.output), snapshot)
    return 0


def atomic_jsonl(path: Path, values: list[dict[str, Any]], append: bool) -> None:
    if not path.is_absolute():
        raise SnapshotError("--output must be an absolute path")
    path.parent.mkdir(parents=True, exist_ok=True)
    payload = "".join(
        json.dumps(value, sort_keys=True, separators=(",", ":")) + "\n"
        for value in values
    )
    if append:
        descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o600)
        try:
            os.fchmod(descriptor, 0o600)
            with os.fdopen(descriptor, "a", encoding="utf-8") as output:
                output.write(payload)
                output.flush()
                os.fsync(output.fileno())
        except BaseException:
            os.close(descriptor)
            raise
        return
    descriptor, temporary = tempfile.mkstemp(prefix=path.name + ".", dir=path.parent)
    try:
        os.fchmod(descriptor, 0o600)
        with os.fdopen(descriptor, "w", encoding="utf-8") as output:
            output.write(payload)
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary, path)
        os.chmod(path, 0o600)
    except BaseException:
        try:
            os.unlink(temporary)
        except OSError:
            pass
        raise


def runtime_socket_snapshot(args: argparse.Namespace) -> int:
    if args.phase not in ("warmup", "main", "drain"):
        raise SnapshotError("runtime-sockets phase must be warmup, main, or drain")
    host = HostView(Path(args.fixture_root).resolve() if args.fixture_root else None)
    kernel = host.uname()
    if kernel["sysname"] != "Linux":
        raise SnapshotError("runtime UDP socket evidence requires native Linux")
    target_bytes, _ = quic_buffer_target(args.quic_go_version)
    if target_bytes is None:
        raise SnapshotError(
            f"no buffer target is known for quic-go {args.quic_go_version}"
        )
    processes: list[tuple[str, int]] = list(args.process or [])
    if not processes:
        raise SnapshotError("runtime-sockets requires at least one --process ROLE:PID")
    sockets = socket_evidence(host, processes, target_bytes)
    if not sockets:
        raise SnapshotError("no attributable live UDP sockets were observed")
    identities: dict[str, tuple[str, str | int]] = {}
    for inode, kind, value in args.socket_identity or []:
        identities[inode] = (kind, value)
    starts = {pid: process_start_ticks(host, pid) for _, pid in processes}
    if any(value is None for value in starts.values()):
        raise SnapshotError("process start ticks are unavailable")
    records: list[dict[str, Any]] = []
    sampled_at = host.captured_at()
    schema_path = (
        Path(__file__).resolve().parent.parent
        / "schemas"
        / "client-runtime-socket-v1.schema.json"
    )
    schema = json.loads(schema_path.read_text(encoding="utf-8"))
    for item in sockets:
        identity = identities.get(item["inode"])
        slot = identity[1] if identity and identity[0] == "slot" else None
        shared = identity[1] if identity and identity[0] == "shared" else None
        receive = item["raw_so_rcvbuf_bytes"]
        send = item["raw_so_sndbuf_bytes"]
        record = {
            "schema_version": 1,
            "kind": "client_runtime_socket",
            "run_id": args.run_id.lower(),
            "sampled_at": sampled_at,
            "phase": args.phase,
            "role": item["role"],
            "pid": item["pid"],
            "process_start_ticks": starts[item["pid"]],
            "socket_inode": item["inode"],
            "local_address": item["local_address"],
            "raw_so_rcvbuf_bytes": receive,
            "raw_so_sndbuf_bytes": send,
            "operator_requested_so_rcvbuf_bytes": target_bytes,
            "operator_requested_so_sndbuf_bytes": target_bytes,
            "source": item["source"],
            "linux_kernel_doubling_applies": True,
            "slot": slot,
            "shared_transport_identity": shared,
            "required_minimum_bytes": target_bytes,
            "meets_buffer_target": receive >= target_bytes and send >= target_bytes,
        }
        validate_schema(record, schema)
        records.append(record)
    atomic_jsonl(Path(args.output), records, args.append)
    return 0 if all(record["meets_buffer_target"] for record in records) else 3


def unsigned_delta(
    old_text: str, new_text: str, width_bits: int
) -> tuple[str | None, str]:
    old = int(old_text)
    new = int(new_text)
    value_count = 1 << width_bits
    if old >= value_count or new >= value_count:
        return None, "out_of_range"
    if new >= old:
        return str(new - old), "normal"
    top_start = (95 * value_count + 99) // 100
    bottom_end = (5 * value_count - 1) // 100
    if old >= top_start and new <= bottom_end:
        return str(value_count - old + new), "wrap"
    return None, "reset"


def indexed_rows(
    rows: Any, keys: tuple[str, ...]
) -> dict[tuple[Any, ...], dict[str, Any]] | None:
    if not isinstance(rows, list):
        return None
    result: dict[tuple[Any, ...], dict[str, Any]] = {}
    for row in rows:
        if not isinstance(row, dict) or any(key not in row for key in keys):
            return None
        identity = tuple(row[key] for key in keys)
        if identity in result:
            return None
        result[identity] = row
    return result


def point_pair_deltas(
    before: dict[str, Any], after: dict[str, Any]
) -> tuple[dict[str, Any], list[str]]:
    reasons: list[str] = []
    for field in (
        "run_id",
        "host_identity_sha256",
        "boot_id",
        "network_namespace_inode",
    ):
        if before.get(field) != after.get(field):
            reasons.append(f"point_{field}_mismatch")
    if before.get("phase") != "before" or after.get("phase") != "after":
        reasons.append("point_phase_mismatch")
    if not before.get("eligible", False) or not after.get("eligible", False):
        reasons.append("point_snapshot_ineligible")

    before_udp = indexed_rows(
        before.get("udp_counters"), ("source", "name", "width_bits")
    )
    after_udp = indexed_rows(
        after.get("udp_counters"), ("source", "name", "width_bits")
    )
    udp_deltas: list[dict[str, Any]] = []
    if before_udp is None or after_udp is None or set(before_udp) != set(after_udp):
        reasons.append("udp_counter_identity_mismatch")
    else:
        for identity in sorted(before_udp):
            source, name, width = identity
            try:
                delta, classification = unsigned_delta(
                    before_udp[identity]["value"],
                    after_udp[identity]["value"],
                    int(width),
                )
            except (TypeError, ValueError):
                delta, classification = None, "invalid"
            udp_deltas.append(
                {
                    "source": source,
                    "name": name,
                    "width_bits": width,
                    "before": before_udp[identity]["value"],
                    "after": after_udp[identity]["value"],
                    "delta": delta,
                    "classification": classification,
                }
            )
            if classification == "reset":
                reasons.append("udp_counter_reset")
            elif classification == "out_of_range":
                reasons.append("udp_counter_out_of_range")
            elif classification == "invalid":
                reasons.append("udp_counter_invalid")

    before_interfaces = indexed_rows(
        before.get("interfaces"), ("ifindex", "name", "source")
    )
    after_interfaces = indexed_rows(
        after.get("interfaces"), ("ifindex", "name", "source")
    )
    interface_deltas: list[dict[str, Any]] = []
    interface_fields = (
        "rx_bytes",
        "tx_bytes",
        "rx_dropped",
        "tx_dropped",
        "rx_errors",
        "tx_errors",
    )
    if (
        before_interfaces is None
        or after_interfaces is None
        or set(before_interfaces) != set(after_interfaces)
    ):
        reasons.append("interface_identity_mismatch")
    else:
        for identity in sorted(before_interfaces):
            old_row = before_interfaces[identity]
            new_row = after_interfaces[identity]
            width = int(old_row["counter_width_bits"])
            if width != int(new_row["counter_width_bits"]):
                reasons.append("interface_counter_width_mismatch")
                continue
            fields: dict[str, Any] = {}
            for field in interface_fields:
                delta, classification = unsigned_delta(
                    old_row[field], new_row[field], width
                )
                fields[field] = {"delta": delta, "classification": classification}
                if classification == "reset":
                    reasons.append("interface_counter_reset")
                elif classification == "out_of_range":
                    reasons.append("interface_counter_out_of_range")
            interface_deltas.append(
                {
                    "ifindex": identity[0],
                    "name": identity[1],
                    "source": identity[2],
                    "width_bits": width,
                    "fields": fields,
                }
            )

    before_processes = indexed_rows(
        before.get("processes"), ("role", "pid", "start_ticks")
    )
    after_processes = indexed_rows(
        after.get("processes"), ("role", "pid", "start_ticks")
    )
    process_deltas: list[dict[str, Any]] = []
    if (
        before_processes is None
        or after_processes is None
        or set(before_processes) != set(after_processes)
    ):
        reasons.append("process_identity_mismatch")
    else:
        for identity in sorted(before_processes):
            old_row = before_processes[identity]
            new_row = after_processes[identity]
            cpu_delta = int(new_row["cpu_time_ns"]) - int(old_row["cpu_time_ns"])
            if (
                old_row["cgroup_cpu_throttled_ns"] is None
                or new_row["cgroup_cpu_throttled_ns"] is None
            ):
                throttle_delta = None
                reasons.append("cgroup_cpu_throttling_unavailable")
            else:
                throttle_delta = int(new_row["cgroup_cpu_throttled_ns"]) - int(
                    old_row["cgroup_cpu_throttled_ns"]
                )
            if cpu_delta < 0:
                reasons.append("process_cpu_counter_reset")
            if throttle_delta is not None and throttle_delta < 0:
                reasons.append("cgroup_throttle_counter_reset")
            process_deltas.append(
                {
                    "role": identity[0],
                    "pid": identity[1],
                    "start_ticks": identity[2],
                    "cpu_time_ns": str(cpu_delta) if cpu_delta >= 0 else None,
                    "cgroup_cpu_throttled_ns": (
                        str(throttle_delta)
                        if throttle_delta is not None and throttle_delta >= 0
                        else None
                    ),
                }
            )

    for point_name, point in (("before", before), ("after", after)):
        sockets = point.get("sockets")
        target = point.get("quic_udp_buffer_target", {}).get("bytes")
        if not isinstance(sockets, list) or not sockets or not isinstance(target, int):
            reasons.append(f"{point_name}_socket_evidence_unavailable")
            continue
        if any(
            row["raw_so_rcvbuf_bytes"] < target or row["raw_so_sndbuf_bytes"] < target
            for row in sockets
        ):
            reasons.append(f"{point_name}_socket_buffer_below_quic_go_target")

    udp_error_names = {
        "NoPorts",
        "InErrors",
        "RcvbufErrors",
        "SndbufErrors",
        "InCsumErrors",
        "MemErrors",
    }
    if any(
        row["name"] in udp_error_names
        and row["delta"] is not None
        and int(row["delta"]) != 0
        for row in udp_deltas
    ):
        reasons.append("udp_error_delta_nonzero")
    if any(
        field in ("rx_dropped", "tx_dropped", "rx_errors", "tx_errors")
        and evidence["delta"] is not None
        and int(evidence["delta"]) != 0
        for row in interface_deltas
        for field, evidence in row["fields"].items()
    ):
        reasons.append("interface_drop_or_error_delta_nonzero")
    return {
        "udp_deltas": udp_deltas,
        "interface_deltas": interface_deltas,
        "process_deltas": process_deltas,
    }, sorted(set(reasons))


def parse_timestamp(value: str) -> dt.datetime:
    return dt.datetime.fromisoformat(value.replace("Z", "+00:00"))


def compare_snapshots(args: argparse.Namespace) -> int:
    if not args.before or not args.after:
        raise SnapshotError("compare requires --before and --after")
    before = load_json(args.before, None)
    after = load_json(args.after, None)
    if not isinstance(before, dict) or not isinstance(after, dict):
        raise SnapshotError("point snapshots must be JSON objects")
    if before.get("kind") == "server_point" and after.get("kind") == "server_point":
        schema_path = (
            Path(__file__).resolve().parent.parent
            / "schemas"
            / "server-point-v1.schema.json"
        )
        schema = json.loads(schema_path.read_text(encoding="utf-8"))
        validate_schema(before, schema)
        validate_schema(after, schema)
    deltas, reasons = point_pair_deltas(before, after)
    attribution: dict[str, Any]
    if args.dedicated_host:
        attribution = {"mode": "dedicated_host", "idle_baseline": None}
    elif args.isolated_network_namespace:
        attribution = {"mode": "isolated_network_namespace", "idle_baseline": None}
    elif args.idle_before and args.idle_after:
        idle_before = load_json(args.idle_before, None)
        idle_after = load_json(args.idle_after, None)
        if not isinstance(idle_before, dict) or not isinstance(idle_after, dict):
            raise SnapshotError("idle point snapshots must be JSON objects")
        idle_deltas, idle_reasons = point_pair_deltas(idle_before, idle_after)
        idle_nonzero = any(
            row["delta"] is None or int(row["delta"]) != 0
            for row in idle_deltas["udp_deltas"]
        )
        try:
            main_seconds = (
                parse_timestamp(after["captured_at"])
                - parse_timestamp(before["captured_at"])
            ).total_seconds()
            idle_seconds = (
                parse_timestamp(idle_after["captured_at"])
                - parse_timestamp(idle_before["captured_at"])
            ).total_seconds()
            duration_matches = abs(main_seconds - idle_seconds) <= max(
                0.05, main_seconds * 0.05
            )
        except (KeyError, TypeError, ValueError):
            duration_matches = False
        if idle_nonzero:
            reasons.append("idle_udp_noise_nonzero")
        if not duration_matches:
            reasons.append("idle_baseline_duration_mismatch")
        if idle_reasons:
            reasons.append("idle_baseline_invalid")
        attribution = {
            "mode": "idle_baseline",
            "idle_baseline": {
                "before": str(Path(args.idle_before).resolve()),
                "after": str(Path(args.idle_after).resolve()),
                "duration_matches": duration_matches,
                "udp_noise_nonzero": idle_nonzero,
            },
        }
    else:
        reasons.append("udp_counter_attribution_unproven")
        attribution = {"mode": "unattributed", "idle_baseline": None}
    reasons = sorted(set(reasons))
    result = {
        "schema_version": 1,
        "kind": "network_delta",
        "provider_version": PROVIDER_VERSION,
        "run_id": before.get("run_id"),
        "before_captured_at": before.get("captured_at"),
        "after_captured_at": after.get("captured_at"),
        "eligible": not reasons,
        "ineligibility_reasons": reasons,
        "attribution": attribution,
        **deltas,
    }
    atomic_json(Path(args.output), result)
    return 0 if not reasons else 3


def network_snapshot(args: argparse.Namespace) -> int:
    if args.mode == "compare":
        return compare_snapshots(args)
    if not args.phase or not args.run_id:
        raise SnapshotError(f"{args.mode} requires --phase and --run-id")
    if args.mode == "point":
        if args.phase not in ("before", "after"):
            raise SnapshotError("point phase must be before or after")
        return point_snapshot(args)
    return runtime_socket_snapshot(args)


def _type_matches(value: Any, expected: str) -> bool:
    return {
        "object": isinstance(value, dict),
        "array": isinstance(value, list),
        "string": isinstance(value, str),
        "integer": isinstance(value, int) and not isinstance(value, bool),
        "number": isinstance(value, (int, float)) and not isinstance(value, bool),
        "boolean": isinstance(value, bool),
        "null": value is None,
    }[expected]


def validate_schema(value: Any, schema: dict[str, Any], location: str = "$") -> None:
    if "anyOf" in schema:
        failures = []
        for option in schema["anyOf"]:
            try:
                validate_schema(value, option, location)
                break
            except SnapshotError as error:
                failures.append(str(error))
        else:
            raise SnapshotError(
                f"{location}: no anyOf branch matched: {'; '.join(failures)}"
            )
    if "const" in schema and value != schema["const"]:
        raise SnapshotError(f"{location}: expected constant {schema['const']!r}")
    if "enum" in schema and value not in schema["enum"]:
        raise SnapshotError(f"{location}: value is not in enum")
    expected = schema.get("type")
    if expected is not None:
        alternatives = expected if isinstance(expected, list) else [expected]
        if not any(_type_matches(value, item) for item in alternatives):
            raise SnapshotError(f"{location}: expected type {expected!r}")
    if isinstance(value, dict):
        for key in schema.get("required", []):
            if key not in value:
                raise SnapshotError(f"{location}: missing required property {key!r}")
        properties = schema.get("properties", {})
        for key, child in properties.items():
            if key in value:
                validate_schema(value[key], child, f"{location}.{key}")
    elif isinstance(value, list):
        if len(value) < schema.get("minItems", 0):
            raise SnapshotError(f"{location}: too few items")
        if schema.get("uniqueItems") and len(
            {json.dumps(item, sort_keys=True) for item in value}
        ) != len(value):
            raise SnapshotError(f"{location}: items are not unique")
        if "items" in schema:
            for index, item in enumerate(value):
                validate_schema(item, schema["items"], f"{location}[{index}]")
    elif isinstance(value, str):
        if len(value) < schema.get("minLength", 0):
            raise SnapshotError(f"{location}: string is too short")
        if "pattern" in schema and not re.search(schema["pattern"], value):
            raise SnapshotError(f"{location}: string does not match required pattern")
    elif isinstance(value, (int, float)) and not isinstance(value, bool):
        if "minimum" in schema and value < schema["minimum"]:
            raise SnapshotError(f"{location}: value is below minimum")


def atomic_json(path: Path, value: Any) -> None:
    if not path.is_absolute():
        raise SnapshotError("--output must be an absolute path")
    path.parent.mkdir(parents=True, exist_ok=True)
    descriptor, temporary = tempfile.mkstemp(prefix=path.name + ".", dir=path.parent)
    try:
        os.fchmod(descriptor, 0o600)
        with os.fdopen(descriptor, "w", encoding="utf-8") as output:
            json.dump(value, output, indent=2, sort_keys=True)
            output.write("\n")
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary, path)
        os.chmod(path, 0o600)
    except BaseException:
        try:
            os.unlink(temporary)
        except OSError:
            pass
        raise


def load_json(path_value: str | None, default: Any) -> Any:
    if not path_value:
        return default
    return json.loads(Path(path_value).read_text(encoding="utf-8"))


def environment_snapshot(args: argparse.Namespace) -> int:
    if args.rtt_ms is not None and args.rtt_ms < 0:
        raise SnapshotError("--rtt-ms must be non-negative")
    output = Path(args.output)
    host = HostView(Path(args.fixture_root).resolve() if args.fixture_root else None)
    source_root = Path(args.source_root).resolve()
    binary = Path(args.binary).resolve()
    if not binary.is_file():
        raise SnapshotError(f"binary not found: {binary}")
    kernel_full = host.uname()
    model = cpu_model(host)
    logical_cpus = logical_cpu_count(host)
    try:
        sysctls: dict[str, int] | None = udp_sysctls(host)
    except SnapshotError:
        sysctls = None
    try:
        captured_boot_id: str | None = boot_id(host)
    except SnapshotError:
        captured_boot_id = None
    try:
        namespace_inode: str | None = host.namespace_inode()
    except SnapshotError:
        namespace_inode = None
    tree = hash_canonical_files(source_root, {output})
    if host.root is not None and not tree["git_available"]:
        if "source_git_commit" in host.meta:
            tree["git_commit"] = str(host.meta["source_git_commit"])
            tree["git_available"] = True
        if "source_dirty" in host.meta:
            tree["dirty"] = bool(host.meta["source_dirty"])
    go_version, modules = module_versions(binary, source_root, host)
    quic_version = modules.get("github.com/quic-go/quic-go")
    qpack_version = modules.get("github.com/quic-go/qpack")
    target_bytes, target_source = quic_buffer_target(quic_version or "")
    raw_command = load_json(args.command_json, [])
    command = sanitize_argv(raw_command)
    command_identity = hashlib.sha256(
        json.dumps(raw_command, ensure_ascii=False, separators=(",", ":")).encode(
            "utf-8"
        )
    ).hexdigest()
    environment = sanitize_environment(load_json(args.environment_json, {}))
    harness_image = image_identity(args.harness_image, host)
    quic_log = quic_go_log_evidence(args.debug_log)
    target = target_evidence(args.target, host, args.rtt_ms)
    distribution = parse_os_release(host)
    runtime = container_runtime(host)
    governor = frequency_governor(host)

    reasons: list[str] = []
    if kernel_full["sysname"] != "Linux":
        reasons.append("native_linux_required")
    if captured_boot_id is None:
        reasons.append("boot_id_unavailable")
    if namespace_inode is None:
        reasons.append("network_namespace_unavailable")
    if sysctls is None:
        reasons.append("udp_sysctls_unavailable")
    if not tree["git_commit"]:
        reasons.append("git_commit_unavailable")
    if go_version == "unavailable":
        reasons.append("go_version_unavailable")
    if qpack_version is None:
        reasons.append("qpack_version_unavailable")
    if target_bytes is None:
        reasons.append("quic_go_buffer_target_unavailable")
    elif sysctls is not None and (
        sysctls["rmem_max"] < target_bytes or sysctls["wmem_max"] < target_bytes
    ):
        reasons.append("udp_sysctl_max_below_quic_go_target")
    if not command:
        reasons.append("benchmark_command_unavailable")
    if not harness_image["image_id_or_digest"] or not harness_image["config_sha256"]:
        reasons.append("harness_image_identity_unavailable")
    if not distribution:
        reasons.append("distribution_identity_unavailable")
    if not runtime["name"] or not runtime["version"]:
        reasons.append("container_runtime_unavailable")
    if governor == "unavailable":
        reasons.append("cpu_frequency_governor_unavailable")
    if not quic_log["retained"]:
        reasons.append("quic_go_debug_log_unavailable")
    elif quic_log["unresolved_udp_buffer_warning"]:
        reasons.append("unresolved_quic_go_udp_buffer_warning")
    if args.target and target is not None:
        if not target["resolved_addresses"]:
            reasons.append("target_resolution_unavailable")
        if target["measured_rtt_ms"] is None:
            reasons.append("target_rtt_unavailable")

    origin: dict[str, Any] | None = None
    if args.kind == "server_stable":
        if not args.origin_binary or not args.origin_image or not args.origin_config:
            raise SnapshotError(
                "server_stable requires --origin-binary, --origin-image, and --origin-config"
            )
        origin_binary = Path(args.origin_binary).resolve()
        origin_config = Path(args.origin_config).resolve()
        if not origin_binary.is_file() or not origin_config.is_file():
            raise SnapshotError("origin binary or config does not exist")
        origin = {
            "binary_sha256": sha256_file(origin_binary),
            "image_id_or_digest": args.origin_image,
            "config_sha256": sha256_file(origin_config),
        }

    affinity: dict[str, Any] = {}
    for role, pid in (("client", args.client_pid), ("server", args.server_pid)):
        if pid is not None:
            affinity[role] = {"pid": pid, "cpus": process_affinity(host, pid)}

    snapshot: dict[str, Any] = {
        "schema_version": 1,
        "kind": args.kind,
        "provider_version": PROVIDER_VERSION,
        "captured_at": host.captured_at(),
        "eligible": not reasons,
        "ineligibility_reasons": sorted(set(reasons)),
        "host_identity_sha256": host_identity(host, kernel_full, model, logical_cpus),
        "boot_id": captured_boot_id,
        "kernel": {
            "sysname": kernel_full["sysname"],
            "release": kernel_full["release"],
        },
        "distribution": distribution,
        "cpu": {
            "model": model,
            "logical_cpus": logical_cpus,
            "affinity_cpus": current_affinity(host, logical_cpus),
            "frequency_governor": governor,
        },
        "memory_bytes": memory_bytes(host),
        "network_namespace_inode": namespace_inode,
        "udp_sysctls_bytes": sysctls,
        "origin": origin,
        "source_tree": tree,
        "h3fuzz": {
            "binary_path": str(binary),
            "binary_sha256": sha256_file(binary),
            "go_version": go_version,
            "modules": {
                "github.com/quic-go/quic-go": quic_version,
                "github.com/quic-go/qpack": qpack_version,
            },
        },
        "quic_udp_buffer_target": {
            "bytes": target_bytes,
            "dependency": "github.com/quic-go/quic-go",
            "version": quic_version,
            "derivation": target_source,
            "linux_kernel_doubling_applies": kernel_full["sysname"] == "Linux",
        },
        "quic_go_log": quic_log,
        "container_runtime": runtime,
        "harness_image": harness_image,
        "invocation": {
            "argv": command,
            "exact_argv_sha256": command_identity,
            "redaction_applied": command != raw_command,
            "environment_overrides": environment,
        },
        "target": target,
        "wordlist": wordlist_evidence(args.wordlist, args.repeat_mode),
        "affinity": affinity,
    }
    if args.kind == "server_stable":
        schema_path = (
            Path(__file__).resolve().parent.parent
            / "schemas"
            / "server-stable-v1.schema.json"
        )
        validate_schema(snapshot, json.loads(schema_path.read_text(encoding="utf-8")))
    atomic_json(output, snapshot)
    return 0


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="Collect read-only PERF-003 evidence")
    subparsers = parser.add_subparsers(dest="command", required=True)
    environment = subparsers.add_parser("environment")
    environment.add_argument(
        "--kind", choices=("client_stable", "server_stable"), default="client_stable"
    )
    environment.add_argument("--output", required=True)
    environment.add_argument("--fixture-root")
    environment.add_argument("--source-root", required=True)
    environment.add_argument("--binary", required=True)
    environment.add_argument("--origin-binary")
    environment.add_argument("--origin-image")
    environment.add_argument("--origin-config")
    environment.add_argument("--harness-image")
    environment.add_argument("--command-json")
    environment.add_argument("--environment-json")
    environment.add_argument("--debug-log")
    environment.add_argument("--target")
    environment.add_argument("--rtt-ms", type=float)
    environment.add_argument("--wordlist")
    environment.add_argument("--repeat-mode")
    environment.add_argument("--client-pid", type=int)
    environment.add_argument("--server-pid", type=int)
    environment.set_defaults(handler=environment_snapshot)
    network = subparsers.add_parser("network")
    network.add_argument(
        "--mode", choices=("point", "runtime-sockets", "compare"), default="point"
    )
    network.add_argument(
        "--kind", choices=("client_point", "server_point"), default="server_point"
    )
    network.add_argument(
        "--phase", choices=("before", "after", "warmup", "main", "drain")
    )
    network.add_argument("--run-id")
    network.add_argument("--output", required=True)
    network.add_argument("--fixture-root")
    network.add_argument("--process", action="append", type=parse_process_spec)
    network.add_argument(
        "--socket-identity", action="append", type=parse_socket_identity
    )
    network.add_argument("--append", action="store_true")
    network.add_argument("--quic-go-version", default="v0.63.0")
    network.add_argument("--before")
    network.add_argument("--after")
    network.add_argument("--idle-before")
    network.add_argument("--idle-after")
    attribution = network.add_mutually_exclusive_group()
    attribution.add_argument("--dedicated-host", action="store_true")
    attribution.add_argument("--isolated-network-namespace", action="store_true")
    network.set_defaults(handler=network_snapshot)
    return parser


def main() -> int:
    try:
        args = build_parser().parse_args()
        return int(args.handler(args))
    except (SnapshotError, OSError, ValueError, json.JSONDecodeError) as error:
        print(f"perf003 snapshot: {error}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
