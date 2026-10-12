#!/usr/bin/env bash
set -euo pipefail

die() {
  printf 'verify-third-party-quic-go: %s\n' "$*" >&2
  exit 1
}

command -v python3 >/dev/null 2>&1 || die "python3 is required"
command -v git >/dev/null 2>&1 || die "git is required"

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
repo_root=${H3FUZZ_ROOT:-$(CDPATH= cd -- "$script_dir/.." && pwd -P)}
provenance=${H3FUZZ_QUIC_GO_PROVENANCE:-$repo_root/third_party/quic-go.UPSTREAM.json}
fork_dir=${H3FUZZ_QUIC_GO_DIR:-$repo_root/third_party/quic-go}

case ${H3FUZZ_OFFLINE:-0} in
  0|1) ;;
  *) die "H3FUZZ_OFFLINE must be 0 or 1" ;;
esac
case ${H3FUZZ_SKIP_ROOT_MODULE_CHECK:-0} in
  0|1) ;;
  *) die "H3FUZZ_SKIP_ROOT_MODULE_CHECK must be 0 or 1" ;;
esac

tmp_parent=${TMPDIR:-/tmp}
work_dir=$(mktemp -d "${tmp_parent%/}/h3fuzz-quic-go-verify.XXXXXXXX") || die "could not create temporary directory"
cleanup() {
  case ${work_dir:-} in
    */h3fuzz-quic-go-verify.*) rm -rf -- "$work_dir" ;;
    *) printf 'verify-third-party-quic-go: refusing to remove unexpected temporary path: %s\n' "${work_dir:-<empty>}" >&2 ;;
  esac
}
trap cleanup EXIT
trap 'exit 130' HUP INT TERM

python3 - "$repo_root" "$provenance" "$fork_dir" "$work_dir" <<'PY'
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import stat
import subprocess
import sys
import zipfile


def fail(message):
    raise SystemExit(f"verify-third-party-quic-go: {message}")


def run(argv, *, cwd=None, env=None):
    result = subprocess.run(
        argv,
        cwd=cwd,
        env=env,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    if result.returncode != 0:
        detail = result.stderr.strip() or result.stdout.strip() or f"exit status {result.returncode}"
        fail(f"command failed ({' '.join(argv)}): {detail}")
    return result.stdout


def sha256_file(path):
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def require_exact_keys(value, expected, label):
    if not isinstance(value, dict):
        fail(f"{label} must be a JSON object")
    actual = set(value)
    expected = set(expected)
    if actual != expected:
        missing = sorted(expected - actual)
        extra = sorted(actual - expected)
        fail(f"{label} keys differ (missing={missing}, extra={extra})")


def safe_repo_relative(value, label):
    if not isinstance(value, str) or not value:
        fail(f"{label} must be a non-empty string")
    if "\\" in value or "\x00" in value:
        fail(f"{label} is not a safe POSIX path: {value!r}")
    path = PurePosixPath(value)
    if path.is_absolute() or any(part in ("", ".", "..") for part in path.parts):
        fail(f"{label} is not a safe repository-relative path: {value!r}")
    return path


def resolve_under(root, relative, label):
    candidate = root / Path(*relative.parts)
    resolved = candidate.resolve()
    try:
        resolved.relative_to(root)
    except ValueError:
        fail(f"{label} resolves outside the repository: {relative}")
    return candidate


def go_environment():
    env = dict(os.environ)
    env["GOWORK"] = "off"
    if os.environ.get("H3FUZZ_OFFLINE", "0") == "1":
        env["GOTOOLCHAIN"] = "local"
        env["GOPROXY"] = "off"
        env["GOSUMDB"] = "off"
    return env


def validate_metadata(path):
    if path.is_symlink() or not path.is_file():
        fail(f"provenance file is not a regular file: {path}")
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError) as exc:
        fail(f"cannot read provenance JSON {path}: {exc}")

    require_exact_keys(data, ["schema_version", "module", "version", "upstream", "dependencies", "fork"], "provenance")
    if data["schema_version"] != 2:
        fail("unsupported provenance schema_version (expected 2)")
    if data["module"] != "github.com/quic-go/quic-go":
        fail("provenance module must be github.com/quic-go/quic-go")
    if not isinstance(data["version"], str) or not re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+", data["version"]):
        fail("provenance version is not a canonical release version")

    upstream = data["upstream"]
    require_exact_keys(
        upstream,
        [
            "vcs",
            "repository",
            "ref",
            "commit",
            "module_sum",
            "go_mod_sum",
            "zip_url",
            "zip_sha256",
            "license_sha256",
        ],
        "provenance.upstream",
    )
    if upstream["vcs"] != "git" or upstream["repository"] != "https://github.com/quic-go/quic-go":
        fail("unexpected upstream VCS or repository")
    if upstream["ref"] != f"refs/tags/{data['version']}":
        fail("upstream ref does not match version")
    if not re.fullmatch(r"[0-9a-f]{40}", upstream["commit"]):
        fail("upstream commit must be a lowercase 40-character Git object ID")
    for key in ("zip_sha256", "license_sha256"):
        if not re.fullmatch(r"[0-9a-f]{64}", upstream[key]):
            fail(f"provenance.upstream.{key} must be a lowercase SHA-256 digest")
    for key in ("module_sum", "go_mod_sum"):
        if not isinstance(upstream[key], str) or not re.fullmatch(r"h1:[A-Za-z0-9+/]{43}=", upstream[key]):
            fail(f"provenance.upstream.{key} must be a Go h1 checksum")
    expected_url = f"https://proxy.golang.org/github.com/quic-go/quic-go/@v/{data['version']}.zip"
    if upstream["zip_url"] != expected_url:
        fail(f"unexpected upstream zip URL (expected {expected_url})")

    dependencies = data["dependencies"]
    require_exact_keys(dependencies, ["qpack"], "provenance.dependencies")
    qpack = dependencies["qpack"]
    require_exact_keys(qpack, ["module", "version", "module_sum", "go_mod_sum"], "provenance.dependencies.qpack")
    if qpack["module"] != "github.com/quic-go/qpack":
        fail("provenance qpack module must be github.com/quic-go/qpack")
    if qpack["version"] != "v0.6.0":
        fail("provenance qpack version must be v0.6.0")
    for key in ("module_sum", "go_mod_sum"):
        if not isinstance(qpack[key], str) or not re.fullmatch(r"h1:[A-Za-z0-9+/]{43}=", qpack[key]):
            fail(f"provenance.dependencies.qpack.{key} must be a Go h1 checksum")

    fork = data["fork"]
    require_exact_keys(fork, ["path", "patch", "patch_sha256", "changed_files"], "provenance.fork")
    safe_repo_relative(fork["path"], "provenance.fork.path")
    safe_repo_relative(fork["patch"], "provenance.fork.patch")
    if not re.fullmatch(r"[0-9a-f]{64}", fork["patch_sha256"] if isinstance(fork["patch_sha256"], str) else ""):
        fail("provenance.fork.patch_sha256 must be a lowercase SHA-256 digest")
    changed = fork["changed_files"]
    if not isinstance(changed, list) or not changed:
        fail("provenance.fork.changed_files must be a non-empty list")
    checked = [str(safe_repo_relative(item, "provenance.fork.changed_files entry")) for item in changed]
    if checked != sorted(set(checked)):
        fail("provenance.fork.changed_files must be sorted and contain no duplicates")
    return data


def validate_download_metadata(raw, data):
    try:
        downloaded = json.loads(raw)
    except json.JSONDecodeError as exc:
        fail(f"go mod download returned invalid JSON: {exc}")
    upstream = data["upstream"]
    expected = {
        "Path": data["module"],
        "Version": data["version"],
        "Sum": upstream["module_sum"],
        "GoModSum": upstream["go_mod_sum"],
    }
    for key, value in expected.items():
        if downloaded.get(key) != value:
            fail(f"download metadata {key} mismatch: expected {value!r}, got {downloaded.get(key)!r}")
    origin = downloaded.get("Origin")
    if not isinstance(origin, dict):
        fail("download metadata has no Origin object")
    origin_expected = {
        "VCS": upstream["vcs"],
        "URL": upstream["repository"],
        "Hash": upstream["commit"],
        "Ref": upstream["ref"],
    }
    for key, value in origin_expected.items():
        if origin.get(key) != value:
            fail(f"download Origin.{key} mismatch: expected {value!r}, got {origin.get(key)!r}")
    zip_name = downloaded.get("Zip")
    if not isinstance(zip_name, str) or not zip_name:
        fail("download metadata did not provide a module zip path")
    return Path(zip_name)


def find_zip(data, scratch):
    explicit = os.environ.get("H3FUZZ_QUIC_GO_UPSTREAM_ZIP")
    if explicit:
        return Path(os.path.abspath(Path(explicit).expanduser())), "explicit"

    go_tool = os.environ.get("H3FUZZ_GO", "go")
    go_env = go_environment()
    mod_cache = run([go_tool, "env", "GOMODCACHE"], cwd=scratch, env=go_env).strip()
    if not mod_cache:
        fail("go env GOMODCACHE returned an empty path")
    cached = Path(mod_cache) / "cache" / "download" / "github.com" / "quic-go" / "quic-go" / "@v" / f"{data['version']}.zip"
    if cached.is_file() and not cached.is_symlink():
        return cached, "module cache"
    if os.environ.get("H3FUZZ_OFFLINE", "0") == "1":
        fail(f"offline mode: upstream module zip is not cached at {cached}")

    raw = run(
        [go_tool, "mod", "download", "-json", f"{data['module']}@{data['version']}"],
        cwd=scratch,
        env=go_env,
    )
    return validate_download_metadata(raw, data), "go mod download"


def validate_patch(path, data):
    if path.is_symlink() or not path.is_file():
        fail(f"patch is not a regular file: {path}")
    actual_sha = sha256_file(path)
    expected_sha = data["fork"]["patch_sha256"]
    if actual_sha != expected_sha:
        fail(f"patch SHA-256 mismatch: expected {expected_sha}, got {actual_sha}")
    try:
        lines = path.read_text(encoding="utf-8").splitlines()
    except (OSError, UnicodeError) as exc:
        fail(f"cannot read patch {path}: {exc}")
    if any(line == "GIT binary patch" or line.startswith(("rename from ", "rename to ", "copy from ", "copy to ", "Binary files ")) for line in lines):
        fail("patch may not contain binary, rename, or copy operations")

    paths = []
    for line in lines:
        if not line.startswith("diff --git "):
            continue
        match = re.fullmatch(r"diff --git a/([^\s]+) b/([^\s]+)", line)
        if not match:
            fail(f"unsupported or unsafe diff header: {line}")
        old, new = match.groups()
        if old != new:
            fail(f"patch changes a path name without an explicit same-path diff: {old!r} -> {new!r}")
        safe_repo_relative(old, "patch path")
        paths.append(old)
    if not paths:
        fail("patch contains no diff sections")
    if len(paths) != len(set(paths)):
        fail("patch contains duplicate diff sections")
    if sorted(paths) != data["fork"]["changed_files"]:
        fail(
            "patch changed-file set does not match provenance: "
            f"patch={sorted(paths)}, provenance={data['fork']['changed_files']}"
        )
    return actual_sha


def extract_zip(zip_path, destination, data):
    if zip_path.is_symlink() or not zip_path.is_file():
        fail(f"upstream module zip is not a regular file: {zip_path}")
    actual_sha = sha256_file(zip_path)
    expected_sha = data["upstream"]["zip_sha256"]
    if actual_sha != expected_sha:
        fail(f"upstream zip SHA-256 mismatch: expected {expected_sha}, got {actual_sha}")

    prefix = f"{data['module']}@{data['version']}/"
    seen = set()
    casefolded = set()
    try:
        archive = zipfile.ZipFile(zip_path)
    except (OSError, zipfile.BadZipFile) as exc:
        fail(f"cannot open upstream module zip: {exc}")
    with archive:
        for entry in archive.infolist():
            name = entry.filename
            if not name.startswith(prefix):
                fail(f"zip entry has unexpected module prefix: {name!r}")
            relative_name = name[len(prefix):]
            if not relative_name:
                continue
            if "\\" in relative_name or "\x00" in relative_name:
                fail(f"unsafe zip entry: {name!r}")
            relative = PurePosixPath(relative_name.rstrip("/"))
            if relative.is_absolute() or any(part in ("", ".", "..") for part in relative.parts):
                fail(f"unsafe zip entry: {name!r}")
            normalized = str(relative)
            if normalized in seen:
                fail(f"duplicate zip entry: {normalized}")
            if normalized.casefold() in casefolded:
                fail(f"case-colliding zip entry: {normalized}")
            seen.add(normalized)
            casefolded.add(normalized.casefold())

            mode = (entry.external_attr >> 16) & 0xFFFF
            file_type = stat.S_IFMT(mode)
            if file_type not in (0, stat.S_IFREG, stat.S_IFDIR):
                fail(f"zip entry is not a regular file or directory: {name!r}")
            target = destination.joinpath(*relative.parts)
            if entry.is_dir() or name.endswith("/"):
                target.mkdir(parents=True, exist_ok=True)
                continue
            target.parent.mkdir(parents=True, exist_ok=True)
            with archive.open(entry, "r") as source, target.open("wb") as output:
                shutil.copyfileobj(source, output)
    return actual_sha


def collect_tree(root, label):
    if root.is_symlink() or not root.is_dir():
        fail(f"{label} is not a directory: {root}")
    files = {}
    for current, dirs, names in os.walk(root, topdown=True, followlinks=False):
        current_path = Path(current)
        for name in list(dirs):
            path = current_path / name
            if path.is_symlink():
                fail(f"{label} contains a symlink: {path.relative_to(root)}")
            if not path.is_dir():
                fail(f"{label} contains a non-directory entry: {path.relative_to(root)}")
        for name in names:
            path = current_path / name
            relative = path.relative_to(root).as_posix()
            try:
                mode = path.lstat().st_mode
            except OSError as exc:
                fail(f"cannot stat {label} entry {relative}: {exc}")
            if not stat.S_ISREG(mode):
                fail(f"{label} contains a non-regular file: {relative}")
            files[relative] = sha256_file(path)
    return files


def compare_trees(expected_root, actual_root):
    expected = collect_tree(expected_root, "reconstructed upstream-plus-patch tree")
    actual = collect_tree(actual_root, "committed quic-go fork")
    missing = sorted(set(expected) - set(actual))
    extra = sorted(set(actual) - set(expected))
    modified = sorted(path for path in set(expected) & set(actual) if expected[path] != actual[path])
    if missing or extra or modified:
        details = []
        details.extend(f"missing: {path}" for path in missing)
        details.extend(f"extra: {path}" for path in extra)
        details.extend(f"modified: {path}" for path in modified)
        shown = details[:20]
        suffix = "" if len(details) <= len(shown) else f"\n  ... and {len(details) - len(shown)} more"
        fail("fork differs from the closed-world reconstruction:\n  " + "\n  ".join(shown) + suffix)
    return len(actual)


def read_go_mod(module_dir, label, go_tool, env):
    go_mod = module_dir / "go.mod"
    if go_mod.is_symlink() or not go_mod.is_file():
        fail(f"{label} go.mod is not a regular file: {go_mod}")
    try:
        return json.loads(run([go_tool, "mod", "edit", "-json", str(go_mod)], cwd=module_dir, env=env))
    except json.JSONDecodeError as exc:
        fail(f"{label} go mod edit returned invalid JSON: {exc}")


def validate_qpack_pin(edited, label, data):
    qpack = data["dependencies"]["qpack"]
    requires = [item for item in edited.get("Require", []) if item.get("Path") == qpack["module"]]
    if len(requires) != 1 or requires[0].get("Version") != qpack["version"]:
        fail(f"{label} go.mod must require {qpack['module']} {qpack['version']} exactly once")
    if any(item.get("Old", {}).get("Path") == qpack["module"] for item in edited.get("Replace") or []):
        fail(f"{label} go.mod must not replace {qpack['module']}")


def validate_selected_qpack(root, data, go_tool, env):
    qpack = data["dependencies"]["qpack"]
    raw = run(
        [
            go_tool,
            "list",
            f"-modfile={root / 'go.mod'}",
            "-mod=readonly",
            "-m",
            "-json",
            qpack["module"],
        ],
        cwd=root,
        env=env,
    )
    try:
        selected = json.loads(raw)
    except json.JSONDecodeError as exc:
        fail(f"go list returned invalid qpack module JSON: {exc}")
    if selected.get("Replace") is not None:
        fail("selected qpack module must not be replaced")
    expected = {
        "Path": qpack["module"],
        "Version": qpack["version"],
        "Sum": qpack["module_sum"],
        "GoModSum": qpack["go_mod_sum"],
    }
    for key, value in expected.items():
        if selected.get(key) != value:
            fail(f"selected qpack {key} mismatch: expected {value!r}, got {selected.get(key)!r}")


def validate_modules(root, fork_dir, data):
    go_tool = os.environ.get("H3FUZZ_GO", "go")
    env = go_environment()
    fork_edited = read_go_mod(fork_dir, "fork", go_tool, env)
    validate_qpack_pin(fork_edited, "fork", data)

    if os.environ.get("H3FUZZ_SKIP_ROOT_MODULE_CHECK", "0") == "1":
        return
    edited = read_go_mod(root, "root", go_tool, env)
    validate_qpack_pin(edited, "root", data)

    requires = [item for item in edited.get("Require", []) if item.get("Path") == data["module"]]
    if len(requires) != 1 or requires[0].get("Version") != data["version"]:
        fail(f"root go.mod must require {data['module']} {data['version']} exactly once")
    replaces = [item for item in edited.get("Replace") or [] if item.get("Old", {}).get("Path") == data["module"]]
    if len(replaces) != 1:
        fail(f"root go.mod must contain exactly one replace for {data['module']}")
    replacement = replaces[0]
    if replacement.get("Old", {}).get("Version"):
        fail("root quic-go replace must apply to every version, not one version only")
    new = replacement.get("New", {})
    if new.get("Version") or not isinstance(new.get("Path"), str):
        fail("root quic-go replace must point to a local directory")
    replacement_path = (root / new["Path"]).resolve() if not Path(new["Path"]).is_absolute() else Path(new["Path"]).resolve()
    if replacement_path != fork_dir.resolve():
        fail(f"root quic-go replace resolves to {replacement_path}, expected {fork_dir.resolve()}")
    validate_selected_qpack(root, data, go_tool, env)


repo_root = Path(sys.argv[1]).resolve()
provenance_path = Path(os.path.abspath(sys.argv[2]))
fork_dir = Path(os.path.abspath(sys.argv[3]))
work_dir = Path(sys.argv[4]).resolve()

if not repo_root.is_dir():
    fail(f"repository root is not a directory: {repo_root}")
data = validate_metadata(provenance_path)

fork_override = os.environ.get("H3FUZZ_QUIC_GO_DIR")
if not fork_override:
    expected_fork = resolve_under(repo_root, safe_repo_relative(data["fork"]["path"], "provenance.fork.path"), "fork path")
    if fork_dir.resolve() != expected_fork.resolve():
        fail(f"fork path does not match provenance: {fork_dir} != {expected_fork}")

patch_override = os.environ.get("H3FUZZ_QUIC_GO_PATCH")
if patch_override:
    patch_path = Path(os.path.abspath(Path(patch_override).expanduser()))
else:
    patch_path = resolve_under(repo_root, safe_repo_relative(data["fork"]["patch"], "provenance.fork.patch"), "patch path")

patch_sha = validate_patch(patch_path, data)
zip_path, zip_source = find_zip(data, work_dir)
expected_tree = work_dir / "expected"
expected_tree.mkdir()
zip_sha = extract_zip(zip_path, expected_tree, data)

license_path = expected_tree / "LICENSE"
if not license_path.is_file() or sha256_file(license_path) != data["upstream"]["license_sha256"]:
    fail("upstream LICENSE SHA-256 does not match provenance")

run(["git", "apply", "--check", "--whitespace=error-all", str(patch_path)], cwd=expected_tree)
run(["git", "apply", "--whitespace=error-all", str(patch_path)], cwd=expected_tree)
file_count = compare_trees(expected_tree, fork_dir)
validate_modules(repo_root, fork_dir, data)

print(
    "verified quic-go fork: "
    f"{file_count} files; upstream zip sha256={zip_sha}; patch sha256={patch_sha}; "
    f"qpack={data['dependencies']['qpack']['version']}; source={zip_source}"
)
PY
