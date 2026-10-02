#!/usr/bin/env python3
"""Create ONLY a new disposable Kind cluster; never use an ambient kubeconfig.

The separate manual fixture generator supplies native fixtures. This bootstrap
installs enforcing Cilium and the repository CRDs, not a release installation.
An absolute new state directory and immutable node image are mandatory.
"""
import argparse
import json
import os
from pathlib import Path
import re
import subprocess


def run(*args, **kwargs):
    return subprocess.run(args, check=True, text=True, **kwargs)


def validate(name, state, image):
    if not re.fullmatch(r"hermes-celln-[a-z0-9][a-z0-9-]{0,30}", name):
        raise ValueError("new hermes-celln-* name required")
    if not state.is_absolute() or state.exists():
        raise ValueError("absolute nonexistent state directory required")
    if not re.fullmatch(r"[A-Za-z0-9._/+-]+:v[0-9]+\.[0-9]+\.[0-9]+@sha256:[0-9a-f]{64}", image):
        raise ValueError("version-tagged AND digest-pinned node image required (Kind uses tag for kubeadm API selection)")


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--name", required=True)
    p.add_argument("--state", type=Path, required=True)
    p.add_argument("--node-image", required=True)
    p.add_argument("--cilium-version", default="1.18.2")
    args = p.parse_args()
    validate(args.name, args.state, args.node_image)
    if args.name in run("kind", "get", "clusters", capture_output=True).stdout.split():
        raise ValueError("refusing existing cluster")
    # Kind runs privileged; the caller need not belong to the host KVM group.
    # Do not chmod/chown the host device. Native execution must prove usability.
    if not Path("/dev/kvm").is_char_device():
        raise ValueError("KVM character device required")
    mem = dict(line.split(":", 1) for line in Path("/proc/meminfo").read_text().splitlines())
    if int(mem["MemAvailable"].split()[0]) < 12 * 1024 * 1024:
        raise ValueError("12 GiB available memory required; coordinate concurrent tests")
    kernel = os.uname().release
    for path in [f"/boot/vmlinuz-{kernel}", f"/lib/modules/{kernel}"]:
        if not Path(path).exists():
            raise ValueError("exact running kernel/modules unavailable")
    args.state.mkdir(mode=0o700)
    state = args.state.resolve()
    config = {"kind": "Cluster", "apiVersion": "kind.x-k8s.io/v1alpha4",
              "networking": {"disableDefaultCNI": True},
              "nodes": [{"role": "control-plane", "extraMounts": [
                  {"hostPath": str(state), "containerPath": str(state)},
                  {"hostPath": "/dev/kvm", "containerPath": "/dev/kvm"},
                  {"hostPath": f"/boot/vmlinuz-{kernel}", "containerPath": f"/boot/vmlinuz-{kernel}", "readOnly": True},
                  {"hostPath": f"/lib/modules/{kernel}", "containerPath": f"/lib/modules/{kernel}", "readOnly": True}]}]}
    (state / "kind.json").write_text(json.dumps(config, indent=2))
    kubeconfig = str(state / "kubeconfig")
    env = dict(os.environ, KUBECONFIG=kubeconfig)
    run("kind", "create", "cluster", "--name", args.name, "--image", args.node_image,
        "--config", str(state / "kind.json"), "--kubeconfig", kubeconfig, env=env)
    node = args.name + "-control-plane"
    # Bound this cluster without touching other containers or host settings.
    run("docker", "update", "--memory", "8g", "--memory-swap", "8g", "--cpus", "4", node)
    ctx = "kind-" + args.name
    kube = ["kubectl", "--kubeconfig", kubeconfig, "--context", ctx]
    run("helm", "install", "cilium", "cilium", "--repo", "https://helm.cilium.io/",
        "--version", args.cilium_version, "--namespace", "kube-system",
        "--kubeconfig", kubeconfig, "--kube-context", ctx,
        "--set", "ipam.mode=kubernetes", "--set", "operator.replicas=1",
        "--set", "image.pullPolicy=IfNotPresent", "--wait", "--timeout", "5m", env=env)
    run(*kube, "-n", "kube-system", "rollout", "status", "daemonset/cilium", "--timeout=600s")
    run(*kube, "wait", "--for=condition=Ready", "node/" + node, "--timeout=600s")
    # Single node dedicated to this review; ordinary review pods can schedule.
    node_info = json.loads(run(*kube, "get", "node", node, "-o", "json", capture_output=True).stdout)
    if any(t["key"] == "node-role.kubernetes.io/control-plane" and t["effect"] == "NoSchedule" for t in node_info["spec"].get("taints", [])):
        run(*kube, "taint", "node", node, "node-role.kubernetes.io/control-plane:NoSchedule-")
    repo = Path(__file__).resolve().parents[3]
    run(*kube, "create", "-f", str(repo / "config/crd/bases"))
    identity = json.loads(run(*kube, "get", "namespace", "kube-system", "-o", "json", capture_output=True).stdout)
    record = {"installedAcceptance": False, "cluster": args.name, "context": ctx,
              "kubeconfig": kubeconfig, "node": node, "nodeImage": args.node_image,
              "ciliumVersion": args.cilium_version, "kubeSystemUID": identity["metadata"]["uid"],
              "scope": "single-host disposable development fixture, not normal release installation"}
    (state / "cluster.json").write_text(json.dumps(record, indent=2))
    print(json.dumps(record, indent=2))


if __name__ == "__main__":
    main()
