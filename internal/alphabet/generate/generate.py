#!/usr/bin/env python3
"""Regenerate the pinned alphabet from Mojang's vanilla 1.20.1 server.

Requires Python 3.9+ and a JDK 17+ (the source launcher runs Probe.java).
Network access is needed only for this explicit maintenance command, not Schemata.
"""

import argparse
import hashlib
import itertools
import json
import os
from pathlib import Path
import subprocess
import tempfile
import urllib.request

VERSION_URL = "https://piston-meta.mojang.com/v1/packages/760a390e336b2b6202ec5906757cee3a929a7c85/1.20.1.json"
SERVER_SHA1 = "84194a2f286ef7c14ed7ce0090dba59902951553"
MAPPINGS_SHA1 = "0b4dba049482496c507b2387a73a913230ebbd76"
SCHEMA_SHA256 = "f7b3dbb683023fff9acb7672aa5efa35f9c1cd6e4a03a2b91e21b095eb3477a0"
PATTERNS_SHA256 = "63b225bdbb97603bbc93cd351afe82618915370724e69a47fbffe5523d32258a"
STATES_SHA256 = "caa0de3f16bab78ef7df9f6e4350bc2df3e17f314e7f4af20f0434506c84bdcc"
SOLID_SHA256 = "396b090e55ea4e9f93d9ef7a0ee1e0002e19746449542729ed48b40f4c40c516"
PACKED_SHA256 = "906c19a2ae7b3aab3680d8d556e1c763227262ab151080cbd4a45708ec45ec82"


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def fetch(url):
    with urllib.request.urlopen(url) as response:
        return response.read()


def state_key(name, properties):
    return name + "[" + ",".join(key + "=" + value for key, value in sorted(properties.items())) + "]"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--java", default="java", help="JDK 17+ java executable")
    parser.add_argument("--work", type=Path, help="retain downloaded inputs and reports in this directory")
    args = parser.parse_args()
    work = (args.work or Path(tempfile.mkdtemp(prefix="schemata-1201-"))).resolve()
    work.mkdir(parents=True, exist_ok=True)
    source = Path(__file__).resolve().parent
    output = source.parent
    metadata = fetch(VERSION_URL)
    require(hashlib.sha1(metadata).hexdigest() == "760a390e336b2b6202ec5906757cee3a929a7c85", "version metadata SHA-1 mismatch")
    (work / "version.json").write_bytes(metadata)
    version = json.loads(metadata)
    for kind, filename, expected in [
        ("server", "server.jar", SERVER_SHA1),
        ("server_mappings", "server.txt", MAPPINGS_SHA1),
    ]:
        download = version["downloads"][kind]
        require(download["sha1"] == expected, "version metadata changed")
        data = fetch(download["url"])
        require(hashlib.sha1(data).hexdigest() == expected, filename + " SHA-1 mismatch")
        require(len(data) == download["size"], filename + " size mismatch")
        (work / filename).write_bytes(data)
        print(filename, "SHA-256", hashlib.sha256(data).hexdigest())
    java = str(Path(args.java).resolve()) if Path(args.java).is_file() else args.java
    subprocess.run([
        java, "-DbundlerMainClass=net.minecraft.data.Main", "-jar", str(work / "server.jar"),
        "--reports", "--output", str(work / "generated"),
    ], cwd=work, check=True)
    report = work / "generated" / "reports"
    blocks = json.loads((report / "blocks.json").read_bytes())
    registries = json.loads((report / "registries.json").read_bytes())
    inner = work / "versions" / "1.20.1" / "server-1.20.1.jar"
    classpath = os.pathsep.join([str(inner)] + sorted(str(p) for p in (work / "libraries").rglob("*.jar")))
    subprocess.run([
        java, "-cp", classpath, str(source / "Probe.java"),
        str(work / "states-live.txt"), str(work / "patterns-live.txt"), str(work / "solid-live.txt"),
    ], cwd=work, check=True)
    live_states = (work / "states-live.txt").read_text(encoding="utf-8").splitlines()
    live_patterns = (work / "patterns-live.txt").read_text(encoding="utf-8").splitlines()
    report_states = []
    canonical_states = []
    schemas = []
    ids = []
    banner_states = 0
    for name, block in sorted(blocks.items()):
        properties = sorted(block.get("properties", {}).items())
        domains = [sorted(values) for _, values in properties]
        expanded = [state_key(name, dict(zip((key for key, _ in properties), combination)))
                    for combination in itertools.product(*domains)]
        actual = [state_key(name, state.get("properties", {})) for state in block["states"]]
        require(len(expanded) == len(actual) and set(expanded) == set(actual), name + " Cartesian state mismatch")
        require(len(actual) == len(set(actual)), name + " duplicate complete state")
        schemas.append("\t".join([name] + [key + "=" + ",".join(sorted(values)) for key, values in properties]))
        canonical_states.extend(expanded)
        report_states.extend(actual)
        ids.extend(state["id"] for state in block["states"])
        if name.endswith("_banner"):
            banner_states += len(actual)
    require(sorted(report_states) == live_states, "live registry differs from report writer")
    require(sorted(ids) == list(range(24135)), "registered state IDs are incomplete")
    require(set(blocks) == set(registries["minecraft:block"]["entries"]), "block registry differs from block report")
    require(len(blocks) == 1003 and len(canonical_states) == 24135 and banner_states == 320, "catalog cardinality changed")
    native_registry_names = [line.split("\t")[1] for line in live_patterns]
    require(set(native_registry_names) == set(registries["minecraft:banner_pattern"]["entries"]), "pattern registry mismatch")
    require(len(live_patterns) == len(set(line.split("\t")[0] for line in live_patterns)) == 41, "pattern count changed")
    canonical = ("\n".join(canonical_states) + "\n").encode()
    require(hashlib.sha256(canonical).hexdigest() == STATES_SHA256, "canonical state fingerprint changed")
    (work / "states-canonical.txt").write_bytes(canonical)
    for filename, lines, expected in [
        ("blocks.tsv", schemas, SCHEMA_SHA256),
        ("patterns.tsv", live_patterns, PATTERNS_SHA256),
    ]:
        data = ("\n".join(lines) + "\n").encode()
        require(hashlib.sha256(data).hexdigest() == expected, filename + " fingerprint changed")
        (output / filename).write_bytes(data)
        print(filename, "SHA-256", expected)
    # Packed version 6: isSolidRender(EmptyBlockGetter.INSTANCE, BlockPos.ZERO)
    # and no block entity, then the first 1,024 in canonical catalog order.
    solid = (work / "solid-live.txt").read_text(encoding="utf-8").splitlines()
    solid_set = set(solid)
    require(len(solid) == len(solid_set) == 1989, "solid-render state count changed")
    require(len({line.split("[")[0] for line in solid}) == 382, "solid-render block count changed")
    selected = [state for state in canonical_states if state in solid_set]
    require(len(selected) == 1989, "solid-render states missing from catalog")
    packed = selected[:1024]
    for filename, lines, expected in [
        ("solid.tsv", sorted(solid), SOLID_SHA256),
        ("packed.tsv", packed, PACKED_SHA256),
    ]:
        data = ("\n".join(lines) + "\n").encode()
        require(hashlib.sha256(data).hexdigest() == expected, filename + " fingerprint changed")
        (output / filename).write_bytes(data)
        print(filename, "SHA-256", expected)
    base = len(live_patterns) * 16
    combinations = sum(base**length for length in range(1, 7))
    print("S=24135 B=320 P=41 K=656 T=" + str(combinations) + " N=" + str(24135 + 320 * combinations))
    print("Independent registry and native banner acceptance validated; inputs retained at", work)


if __name__ == "__main__":
    main()
