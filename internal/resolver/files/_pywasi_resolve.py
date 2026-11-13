"""Pure-Python package selection for pywasi, run inside python.wasm.

Because it runs on the target interpreter, environment markers and
requires-python are evaluated for exactly the environment the packages will
run in. The host mounts a directory with request.json and runs
`python -m _pywasi_resolve DIR`. Requests:

    {"mode": "installed"}
        Distributions already in the interpreter.
    {"mode": "lock", "pylock": "<pylock.toml text>", "prebuilt": {...}}
        Pick a pure-Python wheel (or a prebuilt bundle) for every package of
        a PEP 751 lock that applies to this interpreter.
    {"mode": "resolve", "requirements": [...], "index": "https://pypi.org/simple", "prebuilt": {...}}
        Resolve requirements to pure-Python wheels with resolvelib, reading
        the index (PEP 691 JSON) and wheel metadata (PEP 658).

"prebuilt" lists packages with C extensions that pywasi can link in,
{name: [{"version", "requires"}]}, from its catalog of bundles.

The answer goes to DIR/response.json as {"ok": true, "value": ...} or
{"ok": false, "error": {...}}. Packages in a result are
{"name", "version", "filename", "url", "sha256"} for wheels, or
{"name", "version", "bundle": true} for prebuilt bundles.

There is no network under WASI, so downloads are requested from the host:
a JSON line {"fetch": url, "accept": ..., "immutable": bool} on stdout is
answered on stdin with {"path": file} (the body, in a mounted directory) or
{"error": message}.
"""

import email.parser
import importlib.metadata
import io
import json
import os
import sys
import tomllib
import traceback
import urllib.parse
import zipfile

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "_vendor"))

from packaging.markers import default_environment  # noqa: E402
from packaging.pylock import Pylock  # noqa: E402
from packaging.requirements import Requirement  # noqa: E402
from packaging.specifiers import SpecifierSet  # noqa: E402
from packaging.tags import compatible_tags  # noqa: E402
from packaging.utils import canonicalize_name, parse_wheel_filename  # noqa: E402
from packaging.version import InvalidVersion, Version  # noqa: E402

import resolvelib  # noqa: E402

ENV = default_environment()
PYTHON = Version(ENV["python_full_version"])
# Wheels that run on any platform with this Python: py3-none-any and kin.
PURE_TAGS = frozenset(
    compatible_tags(
        python_version=sys.version_info[:2],
        interpreter=f"cp{sys.version_info[0]}{sys.version_info[1]}",
        platforms=["any"],
    )
)
JSON_INDEX = "application/vnd.pypi.simple.v1+json"


class Error(Exception):
    pass


def fetch(url, accept=None, immutable=False):
    """Bytes at url, downloaded by the host."""
    request = json.dumps({"fetch": url, "accept": accept, "immutable": immutable}) + "\n"
    os.write(1, request.encode())
    reply = json.loads(sys.stdin.buffer.readline() or b'{"error": "host closed the channel"}')
    if "error" in reply:
        raise Error(f"fetching {url}: {reply['error']}")
    with open(reply["path"], "rb") as f:
        return f.read()


def installed():
    """{canonical name: version} of distributions in the interpreter."""
    out = {}
    for dist in importlib.metadata.distributions():
        name = dist.metadata["Name"]
        if name:
            out.setdefault(canonicalize_name(name), dist.version)
    return out


def is_pure(filename):
    if not filename.endswith(".whl"):
        return False
    try:
        _, _, _, tags = parse_wheel_filename(filename)
    except Exception:
        return False
    return not tags.isdisjoint(PURE_TAGS)


# Lock files


def select_lock(text, prebuilt):
    lock = Pylock.from_dict(tomllib.loads(text))
    if lock.requires_python and not lock.requires_python.contains(PYTHON):
        raise Error(f"lock requires Python {lock.requires_python}, the interpreter is {PYTHON}")
    have = installed()
    env = dict(ENV, extras=frozenset(), dependency_groups=frozenset(lock.default_groups or []))
    out, missing = [], []
    for pkg in lock.packages:
        if pkg.marker and not pkg.marker.evaluate(env, context="lock_file"):
            continue
        if pkg.requires_python and not pkg.requires_python.contains(PYTHON):
            raise Error(f"{pkg.name} requires Python {pkg.requires_python}, the interpreter is {PYTHON}")
        name = canonicalize_name(pkg.name)
        if name in have:
            if pkg.version and Version(have[name]) != pkg.version:
                raise Error(f"the lock has {name}=={pkg.version}, but the interpreter has {name}=={have[name]} built in")
            continue
        wheel = next((w for w in pkg.wheels or [] if w.url and is_pure(w.filename)), None)
        if wheel is None and any(str(pkg.version) == b["version"] for b in prebuilt.get(name, [])):
            out.append({"name": name, "version": str(pkg.version), "bundle": True})
            continue
        if wheel is None:
            missing.append(f"{name}=={pkg.version}" if pkg.version else name)
            continue
        if "sha256" not in wheel.hashes:
            raise Error(f"{wheel.filename}: the lock has no sha256")
        out.append(
            {"name": name, "version": str(pkg.version), "filename": wheel.filename, "url": wheel.url, "sha256": wheel.hashes["sha256"]}
        )
    if missing:
        raise Error(no_pure_wheel(missing))
    return out


def no_pure_wheel(names):
    return (
        f"no pure-Python wheel or prebuilt bundle for {', '.join(names)}; packages with C extensions must be "
        "built into the interpreter (use an archive that includes them, or WithDependencies)"
    )


# Resolution


class Candidate:
    def __init__(self, name, version, extras=frozenset(), file=None, installed=False, requires=None):
        self.name, self.version, self.extras = name, version, frozenset(extras)
        self.file, self.installed = file, installed
        self.requires = requires  # Requires-Dist of a prebuilt bundle
        self._deps = None

    def __repr__(self):
        return f"{self.name}{'[' + ','.join(sorted(self.extras)) + ']' if self.extras else ''}=={self.version}"


def identifier(name, extras):
    return f"{name}[{','.join(sorted(extras))}]" if extras else name


class Provider(resolvelib.AbstractProvider):
    def __init__(self, index, prebuilt):
        self.index = index.rstrip("/")
        self.prebuilt = {canonicalize_name(k): v for k, v in prebuilt.items()}
        self.have = installed()
        self.pages = {}
        self.no_pure = set()

    def identify(self, requirement_or_candidate):
        name = canonicalize_name(requirement_or_candidate.name)
        return identifier(name, requirement_or_candidate.extras)

    def get_preference(self, identifier, resolutions, candidates, information, backtrack_causes):
        pinned = any(
            s.operator in ("==", "===") for info in information[identifier] for s in info.requirement.specifier
        )
        return (not pinned, identifier)

    def files(self, name):
        """{Version: file} of pure wheels on the index, newest first."""
        if name not in self.pages:
            url = f"{self.index}/{name}/"
            page = json.loads(fetch(url, accept=JSON_INDEX))
            versions, any_files = {}, False
            for f in page.get("files", []):
                any_files = True
                if f.get("yanked") or not is_pure(f["filename"]):
                    continue
                rp = f.get("requires-python")
                if rp:
                    try:
                        if not SpecifierSet(rp).contains(PYTHON, prereleases=True):
                            continue
                    except Exception:
                        continue
                try:
                    _, version, _, _ = parse_wheel_filename(f["filename"])
                except (ValueError, InvalidVersion):
                    continue
                if "sha256" not in f.get("hashes", {}):
                    continue
                f = dict(f, url=urllib.parse.urljoin(url, f["url"]))
                versions.setdefault(version, f)
            if any_files and not versions:
                self.no_pure.add(name)
            self.pages[name] = dict(sorted(versions.items(), reverse=True))
        return self.pages[name]

    def find_matches(self, identifier, requirements, incompatibilities):
        name = identifier.split("[", 1)[0]
        reqs = list(requirements[identifier])
        extras = frozenset().union(*(r.extras for r in reqs)) if reqs else frozenset()
        bad = {c.version for c in incompatibilities[identifier]}
        if name in self.have:
            version = Version(self.have[name])
            if version in bad or not all(r.specifier.contains(version, prereleases=True) for r in reqs):
                return []
            return [Candidate(name, version, extras, installed=True)]
        if name in self.prebuilt:
            # Packages with C extensions: only versions pywasi can link.
            out = []
            for b in sorted(self.prebuilt[name], key=lambda b: Version(b["version"]), reverse=True):
                version = Version(b["version"])
                if version not in bad and all(r.specifier.contains(version, prereleases=True) for r in reqs):
                    out.append(Candidate(name, version, extras, requires=b.get("requires", [])))
            return out
        out = []
        for version, f in self.files(name).items():
            if version not in bad and all(r.specifier.contains(version) for r in reqs):
                out.append(Candidate(name, version, extras, file=f))
        return out

    def is_satisfied_by(self, requirement, candidate):
        return (
            canonicalize_name(requirement.name) == candidate.name
            and requirement.specifier.contains(candidate.version, prereleases=True)
        )

    def metadata(self, candidate):
        if candidate.requires is not None:
            return candidate.requires
        if candidate.installed:
            return importlib.metadata.distribution(candidate.name).requires or []
        f = candidate.file
        if f.get("core-metadata") or f.get("data-dist-info-metadata"):
            text = fetch(f["url"] + ".metadata", immutable=True).decode()
        else:
            data = fetch(f["url"], immutable=True)
            with zipfile.ZipFile(io.BytesIO(data)) as z:
                (meta,) = [n for n in z.namelist() if n.count("/") == 1 and n.endswith(".dist-info/METADATA")]
                text = z.read(meta).decode()
        return email.parser.Parser().parsestr(text, headersonly=True).get_all("Requires-Dist") or []

    def get_dependencies(self, candidate):
        if candidate._deps is None:
            deps = []
            if candidate.extras:
                # The extras form depends on the plain form of the same version.
                deps.append(Requirement(f"{candidate.name}=={candidate.version}"))
            extras = candidate.extras or {""}
            for line in self.metadata(candidate):
                req = Requirement(line)
                if req.marker and not any(req.marker.evaluate(dict(ENV, extra=e)) for e in extras):
                    continue
                if candidate.extras and req.marker and req.marker.evaluate(dict(ENV, extra="")):
                    continue  # Already a dependency of the plain form.
                req.marker = None
                deps.append(req)
            candidate._deps = deps
        return candidate._deps


def resolve(requirements, index, prebuilt):
    reqs = []
    for line in requirements:
        req = Requirement(line)
        if req.url:
            raise Error(f"{line}: URL requirements are not supported")
        if req.marker and not req.marker.evaluate(ENV):
            continue
        req.marker = None
        reqs.append(req)
    provider = Provider(index, prebuilt)
    try:
        result = resolvelib.Resolver(provider, resolvelib.BaseReporter()).resolve(reqs, max_rounds=1000)
    except resolvelib.ResolutionImpossible as e:
        names = sorted({canonicalize_name(c.requirement.name) for c in e.causes})
        if no_pure := [n for n in names if n in provider.no_pure]:
            raise Error(no_pure_wheel(no_pure)) from None
        reasons = "; ".join(
            f"{c.requirement}" + (f" (required by {c.parent!r})" if c.parent else "") for c in e.causes
        )
        raise Error(f"no versions satisfy {reasons}") from None
    out = []
    for c in sorted(result.mapping.values(), key=lambda c: c.name):
        if c.extras or c.installed:
            continue
        if c.requires is not None:
            out.append({"name": c.name, "version": str(c.version), "bundle": True})
            continue
        f = c.file
        out.append({"name": c.name, "version": str(c.version), "filename": f["filename"], "url": f["url"], "sha256": f["hashes"]["sha256"]})
    return out


# Entry point


def error(exc):
    cls = type(exc)
    name = cls.__qualname__ if cls.__module__ in ("builtins", __name__) else f"{cls.__module__}.{cls.__qualname__}"
    return {"type": name, "message": str(exc), "traceback": "".join(traceback.format_exception(exc))}


def main(directory):
    with open(os.path.join(directory, "request.json")) as f:
        request = json.load(f)
    try:
        match request["mode"]:
            case "installed":
                value = installed()
            case "lock":
                value = select_lock(request["pylock"], request.get("prebuilt") or {})
            case "resolve":
                value = resolve(
                    request["requirements"], request.get("index") or "https://pypi.org/simple", request.get("prebuilt") or {}
                )
            case mode:
                raise Error(f"unknown mode {mode!r}")
        response = {"ok": True, "value": value}
    except Exception as exc:
        response = {"ok": False, "error": error(exc)}
    path = os.path.join(directory, "response.json")
    with open(path + ".tmp", "w") as f:
        json.dump(response, f)
    os.replace(path + ".tmp", path)


if __name__ == "__main__":
    main(sys.argv[1])
