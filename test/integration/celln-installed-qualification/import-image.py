#!/usr/bin/env python3
"""Import a reviewed local image into our Kind node by its actual platform digest.

Docker multi-platform cache may lack other platforms, so Kind's --all-platforms
import fails. This imports linux/amd64 only and obtains the digest from containerd;
never invent a digest from an image config ID or merely rename a mutable image.
"""
import argparse
import json
from pathlib import Path
import subprocess
import sys


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--state", type=Path, required=True)
    p.add_argument("image", help="existing local reviewed Docker image tag")
    args = p.parse_args()
    record = json.loads((args.state / "cluster.json").read_text())
    node = record["node"]
    if not record["cluster"].startswith("hermes-celln-") or node != record["cluster"] + "-control-plane":
        raise ValueError("dedicated cluster identity required")
    k = ["kubectl", "--kubeconfig", record["kubeconfig"], "--context", record["context"]]
    uid = json.loads(subprocess.check_output(k + ["get", "namespace", "kube-system", "-o", "json"]))["metadata"]["uid"]
    if uid != record["kubeSystemUID"]:
        raise ValueError("cluster UID mismatch")
    subprocess.run(["docker", "image", "inspect", args.image], check=True, stdout=subprocess.DEVNULL)
    ctr = ["docker", "exec", "-i", node, "ctr", "-n", "k8s.io", "images"]
    source = subprocess.Popen(["docker", "save", "--platform", "linux/amd64", args.image], stdout=subprocess.PIPE)
    assert source.stdout is not None
    try:
        subprocess.run(ctr + ["import", "--digests", "-"], stdin=source.stdout, stdout=sys.stderr, check=True)
    finally:
        source.stdout.close()
    if source.wait() != 0:
        raise RuntimeError("Docker export failed")
    image = args.image
    first = image.split("/")[0]
    if "/" not in image:
        image = "docker.io/library/" + image
    elif "." not in first and ":" not in first and first != "localhost":
        image = "docker.io/" + image
    listing = subprocess.check_output(ctr + ["list"], text=True)
    entries = [line.split() for line in listing.splitlines()[1:] if line.split() and line.split()[0] == image]
    if len(entries) != 1 or not entries[0][2].startswith("sha256:"):
        raise RuntimeError("imported manifest digest unavailable")
    repository = image.rsplit(":", 1)[0]
    pinned = repository + "@" + entries[0][2]
    subprocess.run(ctr + ["tag", image, pinned], stdout=sys.stderr, check=True)
    # Read back exact target; pod pull-by-digest need not contact a registry.
    if pinned not in subprocess.check_output(ctr + ["list", "-q"], text=True).splitlines():
        raise RuntimeError("digest import verification failed")
    print(pinned)


if __name__ == "__main__":
    main()
