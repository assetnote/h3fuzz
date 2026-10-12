#!/usr/bin/env python3

import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest


SCRIPT_DIR = Path(__file__).resolve().parent
NETWORK_MATRIX = SCRIPT_DIR / "network-matrix.sh"
FIXTURE = SCRIPT_DIR.parent / "tests" / "fixtures" / "network-matrix-profiles.json"


FAKE_NETWORK_TOOL = r'''#!/usr/bin/env python3
import json
import os
from pathlib import Path
import sys

tool = Path(sys.argv[0]).name
args = sys.argv[1:]
state_path = Path(os.environ["FAKE_NETWORK_STATE"])

def empty_state():
    return {
        "next_inode": 9000,
        "namespaces": {},
        "root_links": {},
        "root_qdiscs": {},
        "calls": [],
        "failed_once": [],
    }

def load():
    if not state_path.exists():
        return empty_state()
    return json.loads(state_path.read_text(encoding="utf-8"))

state = load()
invocation = " ".join([tool, *args])
state["calls"].append({"tool": tool, "argv": args})

def save():
    temporary = state_path.with_suffix(".tmp")
    temporary.write_text(json.dumps(state, sort_keys=True), encoding="utf-8")
    os.replace(temporary, state_path)

def maybe_fail():
    match = os.environ.get("FAKE_FAIL_MATCH", "")
    always = os.environ.get("FAKE_FAIL_ALWAYS_MATCH", "")
    if always and always in invocation:
        save()
        raise SystemExit(73)
    if match and match in invocation and match not in state["failed_once"]:
        state["failed_once"].append(match)
        save()
        raise SystemExit(72)

if tool == "uname":
    save()
    print(os.environ.get("FAKE_UNAME", "Linux"))
    raise SystemExit(0)
if tool == "id":
    save()
    print(os.environ.get("FAKE_EUID", "0"))
    raise SystemExit(0)

maybe_fail()

def link_json(name, link):
    return [{
        "ifindex": link.get("ifindex", 10),
        "ifname": name,
        "ifalias": link.get("alias", ""),
        "flags": ["BROADCAST", "UP"] if link.get("up") else ["BROADCAST"],
        "address": "02:00:00:00:00:01",
        "addr_info": link.get("addresses", []),
    }]

def qdisc_json(qdisc):
    if qdisc is None:
        return []
    return [{
        "kind": "netem",
        "handle": "1:",
        "root": True,
        "options": qdisc,
        "bytes": 1234,
        "packets": 100,
        "drops": 0,
        "overlimits": 0,
    }]

def find_root_link(name):
    return state["root_links"].get(name)

def find_namespace_link(namespace, name):
    ns = state["namespaces"].get(namespace)
    if ns is None:
        return None
    return ns["links"].get(name)

def handle_tc(tc_args, namespace=None):
    if tc_args[:3] == ["-j", "-s", "qdisc"] and tc_args[3:5] == ["show", "dev"]:
        dev = tc_args[5]
        qdisc = (
            state["root_qdiscs"].get(dev)
            if namespace is None
            else state["namespaces"][namespace]["qdiscs"].get(dev)
        )
        print(json.dumps(qdisc_json(qdisc)))
        return 0
    if tc_args[:2] == ["qdisc", "add"]:
        dev = tc_args[tc_args.index("dev") + 1]
        if namespace is None:
            if find_root_link(dev) is None:
                return 1
            target = state["root_qdiscs"]
        else:
            if find_namespace_link(namespace, dev) is None:
                return 1
            target = state["namespaces"][namespace]["qdiscs"]
        if dev in target:
            return 2
        target[dev] = {"argv": tc_args}
        return 0
    if tc_args[:2] == ["qdisc", "del"]:
        dev = tc_args[tc_args.index("dev") + 1]
        target = state["root_qdiscs"] if namespace is None else state["namespaces"][namespace]["qdiscs"]
        if dev not in target:
            return 2
        del target[dev]
        return 0
    return 64

def handle_ip(ip_args, namespace=None):
    if namespace is None and ip_args[:2] == ["netns", "list"]:
        for name in sorted(state["namespaces"]):
            print(f"{name} (id: 0)")
        return 0
    if namespace is None and ip_args[:2] == ["netns", "add"] and len(ip_args) == 3:
        name = ip_args[2]
        if name in state["namespaces"]:
            return 1
        state["next_inode"] += 1
        state["namespaces"][name] = {
            "inode": state["next_inode"],
            "links": {"lo": {"alias": "", "up": False, "addresses": []}},
            "qdiscs": {},
        }
        return 0
    if namespace is None and ip_args[:2] == ["netns", "del"] and len(ip_args) == 3:
        name = ip_args[2]
        if name not in state["namespaces"]:
            return 1
        del state["namespaces"][name]
        return 0
    if namespace is None and ip_args[:2] == ["netns", "exec"]:
        name = ip_args[2]
        if name not in state["namespaces"]:
            return 1
        nested = ip_args[3:]
        nested_tool = Path(nested[0]).name
        nested_args = nested[1:]
        if nested_tool == "stat":
            print(state["namespaces"][name]["inode"])
            return 0
        if nested_tool == "ip":
            return handle_ip(nested_args, name)
        if nested_tool == "tc":
            return handle_tc(nested_args, name)
        return 127

    json_mode = ip_args[:3] == ["-j", "-d", "link"]
    if json_mode and ip_args[3:5] == ["show", "dev"]:
        dev = ip_args[5]
        link = find_root_link(dev) if namespace is None else find_namespace_link(namespace, dev)
        if link is None:
            return 1
        print(json.dumps(link_json(dev, link)))
        return 0
    if ip_args[:3] == ["link", "show", "dev"]:
        dev = ip_args[3]
        link = find_root_link(dev) if namespace is None else find_namespace_link(namespace, dev)
        if link is None:
            return 1
        print(f"10: {dev}: <BROADCAST>")
        return 0
    if namespace is None and ip_args[:2] == ["link", "add"]:
        host = ip_args[2]
        peer = ip_args[ip_args.index("name") + 1]
        if host in state["root_links"] or peer in state["root_links"]:
            return 1
        state["root_links"][host] = {"alias": "", "up": False, "addresses": [], "peer": peer}
        state["root_links"][peer] = {"alias": "", "up": False, "addresses": [], "peer": host}
        return 0
    if ip_args[:3] == ["link", "set", "dev"]:
        dev = ip_args[3]
        link = find_root_link(dev) if namespace is None else find_namespace_link(namespace, dev)
        if link is None:
            return 1
        tail = ip_args[4:]
        if tail[:1] == ["alias"]:
            link["alias"] = tail[1]
            return 0
        if tail == ["up"]:
            link["up"] = True
            return 0
        if namespace is None and tail[:1] == ["netns"]:
            target = tail[1]
            if target not in state["namespaces"]:
                return 1
            state["namespaces"][target]["links"][dev] = state["root_links"].pop(dev)
            return 0
        return 64
    if ip_args[:2] == ["address", "add"]:
        address = ip_args[2]
        dev = ip_args[ip_args.index("dev") + 1]
        link = find_root_link(dev) if namespace is None else find_namespace_link(namespace, dev)
        if link is None:
            return 1
        link["addresses"].append({"local": address.split("/")[0], "prefixlen": int(address.split("/")[1])})
        return 0
    if namespace is None and ip_args[:3] == ["link", "del", "dev"]:
        dev = ip_args[3]
        link = state["root_links"].pop(dev, None)
        if link is None:
            return 1
        state["root_qdiscs"].pop(dev, None)
        peer = link.get("peer")
        state["root_links"].pop(peer, None)
        state["root_qdiscs"].pop(peer, None)
        for ns in state["namespaces"].values():
            ns["links"].pop(peer, None)
            ns["qdiscs"].pop(peer, None)
        return 0
    return 64

if tool == "tc":
    result = handle_tc(args)
elif tool == "ip":
    result = handle_ip(args)
else:
    result = 127
save()
raise SystemExit(result)
'''


class NetworkMatrixShellTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        self.fake_bin = self.root / "fake-bin"
        self.fake_bin.mkdir()
        self.fake_tool = self.fake_bin / "fake-network-tool.py"
        self.fake_tool.write_text(FAKE_NETWORK_TOOL, encoding="utf-8")
        self.fake_tool.chmod(0o755)
        for name in ("ip", "tc", "uname", "id"):
            (self.fake_bin / name).symlink_to(self.fake_tool)
        self.state_root = self.root / "state"
        self.fake_state = self.root / "fake-network-state.json"

    def tearDown(self):
        self.temporary.cleanup()

    def environment(self, state_root: Path | None = None) -> dict[str, str]:
        environment = dict(os.environ)
        environment.update(
            {
                "PATH": str(self.fake_bin) + os.pathsep + environment.get("PATH", ""),
                "H3FUZZ_NETWORK_MATRIX_TEST_MODE": "1",
                "H3FUZZ_NETWORK_MATRIX_STATE_DIR": str(state_root or self.state_root),
                "FAKE_NETWORK_STATE": str(self.fake_state),
            }
        )
        return environment

    @staticmethod
    def apply_command(profile_id: str = "rtt25-loss01") -> list[str]:
        return [
            str(NETWORK_MATRIX),
            "apply",
            "--profile-id",
            profile_id,
            "--rtt-ms",
            "25",
            "--loss-percent",
            "0.1",
            "--seed",
            "1001",
            "--peer-stream-limit",
            "64",
            "--wordlist-profile",
            "short",
            "--throttle-rps",
            "50",
            "--throttle-status",
            "429",
            "--failure-profile",
            "transient-link-down",
            "--failure-after-ms",
            "5000",
            "--failure-duration-ms",
            "1000",
        ]

    def run_command(self, command, *, environment=None, **kwargs):
        return subprocess.run(
            command,
            env=environment or self.environment(),
            check=False,
            capture_output=True,
            text=True,
            **kwargs,
        )

    def apply(self, profile_id: str = "rtt25-loss01", environment=None):
        return self.run_command(self.apply_command(profile_id), environment=environment)

    def fake_network(self) -> dict:
        return json.loads(self.fake_state.read_text(encoding="utf-8"))

    def manifest(self, profile_id: str = "rtt25-loss01") -> dict:
        path = self.state_root / "profiles" / profile_id / "profile.json"
        return json.loads(path.read_text(encoding="utf-8"))

    def test_apply_status_cleanup_and_idempotent_cleanup(self):
        applied = self.apply()
        self.assertEqual(applied.returncode, 0, applied.stderr)
        profile = json.loads(applied.stdout)
        self.assertEqual(profile["lifecycle"], "active")
        self.assertEqual(profile["network"]["rtt_ms"], "25")
        self.assertEqual(profile["network"]["one_way_delay_us"], 12500)
        self.assertEqual(
            [item["delay_us"] for item in profile["network"]["directions"]],
            [12500, 12500],
        )
        self.assertEqual(profile["network"]["loss"]["configured_percent"], "0.1")
        self.assertEqual(profile["network"]["loss"]["configured_seed"], 1001)
        self.assertTrue(profile["network"]["loss"]["seed_applied"])
        self.assertEqual(profile["network"]["qdisc_packet_limit"], 1000000)
        self.assertEqual(profile["workload"]["peer_stream_limit"]["configured"], 64)
        self.assertEqual(profile["workload"]["wordlist_profile"], "short")
        self.assertEqual(profile["workload"]["server_throttle"]["response_status"], 429)
        self.assertTrue(profile["workload"]["transient_failure"]["enabled"])
        self.assertTrue(all(isinstance(command, list) for command in profile["commands"]["apply"]))
        self.assertIn("seed", profile["commands"]["apply"][-1])
        self.assertIn("1000000", profile["commands"]["apply"][-1])

        profile_dir = self.state_root / "profiles" / "rtt25-loss01"
        before = json.loads((profile_dir / "before.json").read_text(encoding="utf-8"))
        after_apply = json.loads((profile_dir / "applied.json").read_text(encoding="utf-8"))
        self.assertFalse(before["namespace"]["present"])
        self.assertFalse(before["interfaces"]["host"]["present"])
        self.assertTrue(after_apply["namespace"]["present"])
        self.assertTrue(after_apply["interfaces"]["host"]["present"])
        self.assertTrue(after_apply["interfaces"]["peer"]["present"])
        self.assertEqual(after_apply["interfaces"]["host"]["qdisc"][0]["kind"], "netem")
        self.assertEqual(after_apply["interfaces"]["peer"]["qdisc"][0]["kind"], "netem")

        status = self.run_command([str(NETWORK_MATRIX), "status", "--profile-id", "rtt25-loss01"])
        self.assertEqual(status.returncode, 0, status.stderr)
        status_value = json.loads(status.stdout)
        self.assertEqual(status_value["profile"]["lifecycle"], "active")
        self.assertTrue(status_value["live_state"]["namespace"]["present"])

        cleanup = self.run_command([str(NETWORK_MATRIX), "cleanup", "--profile-id", "rtt25-loss01"])
        self.assertEqual(cleanup.returncode, 0, cleanup.stderr)
        cleaned = json.loads(cleanup.stdout)
        self.assertEqual(cleaned["lifecycle"], "cleaned")
        observed = json.loads((profile_dir / "observed.json").read_text(encoding="utf-8"))
        self.assertTrue(observed["interfaces"]["host"]["observed_loss"]["available"])
        self.assertEqual(observed["interfaces"]["host"]["observed_loss"]["percent"], 0.0)
        self.assertTrue(observed["interfaces"]["peer"]["observed_loss"]["available"])
        after = json.loads((profile_dir / "after.json").read_text(encoding="utf-8"))
        self.assertFalse(after["namespace"]["present"])
        self.assertFalse(after["interfaces"]["host"]["present"])
        mutation_count = len(self.fake_network()["calls"])

        repeated = self.run_command([str(NETWORK_MATRIX), "cleanup", "--profile-id", "rtt25-loss01"])
        self.assertEqual(repeated.returncode, 0, repeated.stderr)
        self.assertEqual(json.loads(repeated.stdout)["lifecycle"], "cleaned")
        # Read-only ownership checks are allowed; no qdisc/link/netns delete is repeated.
        later_calls = self.fake_network()["calls"][mutation_count:]
        self.assertFalse(
            any(
                call["tool"] in {"ip", "tc"}
                and ("del" in call["argv"] or "add" in call["argv"])
                for call in later_calls
            )
        )

    def test_partial_apply_failure_rolls_back_every_owned_resource(self):
        environment = self.environment()
        environment["FAKE_FAIL_MATCH"] = "ip netns exec h3n-"
        # Narrow the match to the peer qdisc after deriving its name from the command log.
        # The generic prefix would fail inode capture, so use the stable tc suffix too.
        environment["FAKE_FAIL_MATCH"] = "qdisc add dev h3p-"
        failed = self.apply("partial-failure", environment=environment)
        self.assertNotEqual(failed.returncode, 0)
        state = self.fake_network()
        self.assertEqual(state["namespaces"], {})
        self.assertEqual(state["root_links"], {})
        self.assertEqual(state["root_qdiscs"], {})
        manifest = self.manifest("partial-failure")
        self.assertEqual(manifest["lifecycle"], "apply_failed_rolled_back")
        profile_dir = self.state_root / "profiles" / "partial-failure"
        self.assertTrue((profile_dir / "before.json").is_file())
        self.assertTrue((profile_dir / "after.json").is_file())
        self.assertFalse((profile_dir / "applied.json").exists())

    def test_cleanup_refuses_wrong_owner_without_mutation(self):
        applied = self.apply("wrong-owner")
        self.assertEqual(applied.returncode, 0, applied.stderr)
        state = self.fake_network()
        manifest = self.manifest("wrong-owner")
        host = manifest["network"]["host_interface"]
        state["root_links"][host]["alias"] = "somebody-else"
        self.fake_state.write_text(json.dumps(state), encoding="utf-8")
        before_calls = len(state["calls"])

        cleanup = self.run_command([str(NETWORK_MATRIX), "cleanup", "--profile-id", "wrong-owner"])
        self.assertEqual(cleanup.returncode, 3, cleanup.stderr)
        self.assertIn("different profile", cleanup.stderr)
        current = self.fake_network()
        self.assertIn(host, current["root_links"])
        new_calls = current["calls"][before_calls:]
        self.assertFalse(any("del" in call["argv"] for call in new_calls))

    def test_cleanup_residue_returns_exit_four(self):
        applied = self.apply("cleanup-residue")
        self.assertEqual(applied.returncode, 0, applied.stderr)
        environment = self.environment()
        environment["FAKE_FAIL_ALWAYS_MATCH"] = "link del dev"
        cleanup = self.run_command(
            [str(NETWORK_MATRIX), "cleanup", "--profile-id", "cleanup-residue"],
            environment=environment,
        )
        self.assertEqual(cleanup.returncode, 4, cleanup.stderr)
        self.assertEqual(self.manifest("cleanup-residue")["lifecycle"], "cleanup_residue")
        self.assertTrue(self.fake_network()["root_links"])

    def test_collisions_and_replaced_namespace_are_refused(self):
        first = self.apply("collision")
        self.assertEqual(first.returncode, 0, first.stderr)
        second = self.apply("collision")
        self.assertEqual(second.returncode, 3, second.stderr)
        self.assertIn("already has a state record", second.stderr)

        state = self.fake_network()
        profile = self.manifest("collision")
        namespace = profile["network"]["namespace"]
        state["namespaces"][namespace]["inode"] += 1
        self.fake_state.write_text(json.dumps(state), encoding="utf-8")
        cleanup = self.run_command([str(NETWORK_MATRIX), "cleanup", "--profile-id", "collision"])
        self.assertEqual(cleanup.returncode, 3, cleanup.stderr)
        self.assertIn("inode does not match", cleanup.stderr)

    def test_command_injection_and_unsafe_paths_fail_before_mutation(self):
        marker = self.root / "injected"
        injected_id = f"bad;touch-{marker}"
        command = self.apply_command(injected_id)
        rejected = self.run_command(command)
        self.assertEqual(rejected.returncode, 2, rejected.stderr)
        self.assertFalse(marker.exists())
        if self.fake_state.exists():
            state = self.fake_network()
            self.assertEqual(state["namespaces"], {})

        relative = self.environment(Path("relative/state"))
        rejected_path = self.run_command(self.apply_command("relative-path"), environment=relative)
        self.assertEqual(rejected_path.returncode, 3, rejected_path.stderr)
        self.assertIn("absolute path", rejected_path.stderr)

        unsafe = self.environment(self.root / "state;touch-injected")
        rejected_unsafe = self.run_command(self.apply_command("unsafe-path"), environment=unsafe)
        self.assertEqual(rejected_unsafe.returncode, 3, rejected_unsafe.stderr)
        self.assertIn("unsafe characters", rejected_unsafe.stderr)

        real_directory = self.root / "real-state"
        real_directory.mkdir()
        symlink = self.root / "linked-state"
        symlink.symlink_to(real_directory, target_is_directory=True)
        rejected_symlink = self.run_command(
            self.apply_command("symlink-path"), environment=self.environment(symlink)
        )
        self.assertEqual(rejected_symlink.returncode, 3, rejected_symlink.stderr)
        self.assertIn("symbolic link", rejected_symlink.stderr)

    def test_zero_loss_is_normalized_and_non_apply_options_are_rejected(self):
        command = self.apply_command("normalized-zero")
        command[command.index("0.1")] = "0.000"
        applied = self.run_command(command)
        self.assertEqual(applied.returncode, 0, applied.stderr)
        profile = json.loads(applied.stdout)
        self.assertEqual(profile["network"]["loss"]["configured_percent"], "0")
        self.assertFalse(profile["network"]["loss"]["seed_applied"])
        self.assertNotIn("seed", profile["commands"]["apply"][-1])

        status = self.run_command(
            [
                str(NETWORK_MATRIX),
                "status",
                "--profile-id",
                "normalized-zero",
                "--peer-stream-limit",
                "64",
            ]
        )
        self.assertEqual(status.returncode, 2, status.stderr)
        self.assertIn("accepts only --profile-id", status.stderr)

    def test_native_linux_and_root_checks_fail_closed(self):
        non_linux = self.environment()
        non_linux["FAKE_UNAME"] = "Darwin"
        result = self.run_command(self.apply_command("not-linux"), environment=non_linux)
        self.assertEqual(result.returncode, 3, result.stderr)
        self.assertIn("native Linux", result.stderr)

        unprivileged = self.environment()
        unprivileged["FAKE_EUID"] = "1000"
        result = self.run_command(self.apply_command("not-root"), environment=unprivileged)
        self.assertEqual(result.returncode, 3, result.stderr)
        self.assertIn("CAP_NET_ADMIN", result.stderr)

    def test_missing_required_tool_fails_before_any_network_mutation(self):
        isolated = self.root / "isolated-bin"
        isolated.mkdir()
        for name in ("ip", "uname", "id"):
            (isolated / name).symlink_to(self.fake_tool)
        executables = {
            "python3": sys.executable,
            **{
                name: shutil.which(name)
                for name in ("date", "stat", "mkdir", "chmod", "rm", "rmdir", "mv", "cat")
            },
        }
        for name, target in executables.items():
            self.assertIsNotNone(target, name)
            (isolated / name).symlink_to(target)
        environment = self.environment()
        environment["PATH"] = str(isolated)
        result = self.run_command(self.apply_command("missing-tc"), environment=environment)
        self.assertEqual(result.returncode, 3, result.stderr)
        self.assertIn("required command not found: tc", result.stderr)
        state = self.fake_network()
        self.assertEqual(state["namespaces"], {})
        self.assertEqual(state["root_links"], {})

    def test_fixture_covers_bounded_mandatory_and_exploratory_matrix(self):
        fixture = json.loads(FIXTURE.read_text(encoding="utf-8"))
        profiles = fixture["profiles"]
        required = {
            "profile_id",
            "class",
            "rtt_ms",
            "loss_percent",
            "seed",
            "peer_stream_limit",
            "wordlist_profile",
            "throttle_rps",
            "throttle_status",
            "failure_profile",
            "failure_after_ms",
            "failure_duration_ms",
        }
        for profile in profiles:
            self.assertEqual(set(profile), required)
            self.assertRegex(profile["profile_id"], r"^[a-z0-9][a-z0-9._-]{0,62}$")
            self.assertIn(profile["class"], {"mandatory-clean", "mandatory-safety", "exploratory"})
            self.assertGreater(profile["rtt_ms"], 0)
            self.assertGreaterEqual(profile["loss_percent"], 0)
            self.assertLessEqual(profile["loss_percent"], 100)
            self.assertEqual((profile["rtt_ms"] * 1000) % 2, 0)
            self.assertIn(profile["wordlist_profile"], {"short", "long"})
            self.assertIn(profile["failure_profile"], {"none", "transient-link-down"})
            self.assertGreaterEqual(profile["throttle_rps"], 0)
            self.assertGreaterEqual(profile["throttle_status"], 100)
            self.assertLessEqual(profile["throttle_status"], 599)
        clean = [item for item in profiles if item["class"] == "mandatory-clean"]
        self.assertEqual(
            {(item["rtt_ms"], item["peer_stream_limit"]) for item in clean},
            {(rtt, streams) for rtt in (1, 25, 50, 100) for streams in (64, 256)},
        )
        self.assertTrue(all(item["loss_percent"] == 0 and item["wordlist_profile"] == "long" for item in clean))
        safety = [item for item in profiles if item["class"] == "mandatory-safety"]
        self.assertTrue(any(item["loss_percent"] == 0.1 for item in safety))
        self.assertTrue(any(item["loss_percent"] == 1 for item in safety))
        self.assertTrue(any(item["failure_profile"] == "transient-link-down" for item in safety))
        self.assertTrue(any(item["throttle_status"] == 429 and item["throttle_rps"] > 0 for item in safety))
        self.assertTrue(any(item["wordlist_profile"] == "short" for item in safety))
        exploratory = [item for item in profiles if item["class"] == "exploratory"]
        self.assertTrue(any(item["rtt_ms"] == 10 for item in exploratory))
        self.assertTrue(any(item["loss_percent"] == 0.01 for item in exploratory))
        self.assertTrue(any(item["peer_stream_limit"] > 256 for item in exploratory))
        ids = [item["profile_id"] for item in profiles]
        self.assertEqual(len(ids), len(set(ids)))


if __name__ == "__main__":
    unittest.main()
