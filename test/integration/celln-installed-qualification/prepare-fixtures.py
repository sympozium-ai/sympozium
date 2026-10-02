#!/usr/bin/env python3
"""Generate/adapt the existing reviewed fixture ONLY inside our new Kind cluster.

Does not discover credentials. Uses generated, explicitly fake provider tokens.
Requires bootstrap-kind.py's persisted cluster UID and an exact matching context.
No existing fixture resources are replaced. This is not the normal Helm install.
"""
import argparse
import json
import os
from pathlib import Path
import subprocess


def run(*args, **kwargs):
    return subprocess.run(args, check=True, text=True, **kwargs)


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--state", type=Path, required=True)
    p.add_argument("--package", type=Path, required=True)
    p.add_argument("--image", required=True)
    p.add_argument("--postgres-image", required=True)
    p.add_argument("--review", default="495")
    args = p.parse_args()
    state = args.state.resolve()
    identity = json.loads((state / "cluster.json").read_text())
    if not identity["cluster"].startswith("hermes-celln-"):
        raise ValueError("not a dedicated bootstrap cluster")
    kube = ["kubectl", "--kubeconfig", identity["kubeconfig"], "--context", identity["context"]]
    current = json.loads(run(*kube, "get", "namespace", "kube-system", "-o", "json", capture_output=True).stdout)
    if current["metadata"]["uid"] != identity["kubeSystemUID"]:
        raise ValueError("cluster identity changed")
    node = json.loads(run(*kube, "get", "node", identity["node"], "-o", "json", capture_output=True).stdout)
    if node["metadata"]["name"] != identity["cluster"] + "-control-plane":
        raise ValueError("node identity mismatch")
    repo = Path(__file__).resolve().parents[3]
    output = state / "fixtures"
    env = dict(os.environ, KUBECONFIG=identity["kubeconfig"], CELLN_REVIEW_KUBE_CONTEXT=identity["context"], GOMAXPROCS="2", GOFLAGS="-mod=readonly -p=2")
    env.setdefault("GOCACHE", str(state / "go-cache"))
    run(str(repo / "hack/generate-celln-framework-review.sh"), "--review", args.review,
        "--image", args.image, "--postgres-image", args.postgres_image,
        "--package", str(args.package.resolve()), "--private-state", str(state), "--output", str(output), env=env)
    # Make the historical framework-only fixture portable without changing its
    # authority/protocol/provider/limits. Kind's built-in durable provisioner is
    # named standard; data survives pod restarts, not deleting this cluster.
    components = output / "30-components.yaml"
    raw = components.read_text()
    assert "nodeName: framework" in raw and "storageClassName: local-path" in raw
    raw = raw.replace("nodeName: framework", "nodeName: " + identity["node"])
    raw = raw.replace("kubernetes.io/hostname: framework", "kubernetes.io/hostname: " + identity["node"])
    raw = raw.replace("storageClassName: local-path", "storageClassName: standard")
    components.write_text(raw)
    tenants = output / "40-tenants.yaml"
    tenants_raw = tenants.read_text()
    # Current scoped resolver requires the Agent owner to explicitly lend the
    # named fixture credential; naming a ModelConnection alone grants nothing.
    tenants_raw = tenants_raw.replace("  runtimeRef: review-runtime\n", "  runtimeRef: review-runtime\n  authRefs: [{provider: review-fixture, secret: review-provider-credential}]\n")
    # The historical walkthrough used B only for enduring. This runner exercises
    # identical one-shot declarations in A/B. Enduring remains a separate gate.
    package_meta = json.loads((args.package / "package.json").read_text())
    revisions = {p["name"]: p["revision"] for p in package_meta["resources"]["profiles"]}
    old_ref = "name: celln-json-enduring-review-" + args.review + ", revision: " + revisions["celln-json-enduring"]
    new_ref = "name: celln-json-one-shot-review-" + args.review + ", revision: " + revisions["celln-json-one-shot"]
    if tenants_raw.count(old_ref) != 1:
        raise ValueError("historical B profile template changed; review adapter")
    tenants_raw = tenants_raw.replace(old_ref, new_ref)
    tenants.write_text(tenants_raw)
    for suffix in ["system", "a", "b", "denied"]:
        labels = {"sympozium.ai/celln-review": args.review}
        if suffix in ["a", "b"]:
            labels["sympozium.ai/celln-review-tenant"] = "enabled"
        obj = {"apiVersion": "v1", "kind": "Namespace", "metadata": {"name": f"celln-review-{suffix}-{args.review}", "labels": labels}}
        run(*kube, "create", "-f", "-", input=json.dumps(obj))
    run(str(output / "deploy.sh"), env=env)
    for suffix, component in [("system", "postgres"), ("system", "gateway"), ("system", "native"), ("system", "controller"), ("a", "provider"), ("b", "provider")]:
        deployment = f"review-provider-{args.review}" if component == "provider" else f"celln-review-{component}-{args.review}"
        run(*kube, "-n", f"celln-review-{suffix}-{args.review}", "rollout", "status", "deployment/" + deployment, "--timeout=180s")
    print("Reviewed deterministic fixtures ready; NOT real-provider/release acceptance")


if __name__ == "__main__":
    main()
