#!/usr/bin/env python3
"""Read back native tombstones, PostgreSQL ledger, and disposable cluster inventory.

Only supplements this runner's deterministic fixture evidence. Not A01-A12
certification; ledger reservations are NOT an independent provider call counter.
Never reads Kubernetes Secrets, ambient provider credentials, or full pod logs.
"""
import argparse
import csv
import hashlib
import io
import json
import os
from pathlib import Path
import subprocess


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--state", type=Path, required=True)
    p.add_argument("--report", type=Path, required=True)
    p.add_argument("--output", type=Path, required=True)
    args = p.parse_args()
    if args.output.exists():
        raise ValueError("new output path required")
    cluster = json.loads((args.state / "cluster.json").read_text())
    report = json.loads(args.report.read_text())
    if not cluster["cluster"].startswith("hermes-celln-"):
        raise ValueError("not our disposable cluster")
    kube = ["kubectl", "--kubeconfig", cluster["kubeconfig"], "--context", cluster["context"]]
    def get(*a):
        return subprocess.check_output(kube + list(a), text=True)
    live = json.loads(get("get", "namespace", "kube-system", "-o", "json"))
    if live["metadata"]["uid"] != cluster["kubeSystemUID"]:
        raise ValueError("cluster changed")
    if len(report["runs"]) != 4 or not all(c["passed"] for c in report["checks"]) or report["recoveryRequired"]:
        raise ValueError("collector requires the complete four-run partial journey, not release acceptance")
    system = "celln-review-system-495"
    def native(*a):
        return get("-n", system, "exec", "deployment/celln-review-native-495", "-c", "native", "--", *a)
    sql = "SELECT run_uid,budget_id,namespace_uid,reserved_requests,reserved_output_tokens,observed_output_tokens,closed FROM celln_model_budgets ORDER BY created_at"
    ledger = list(csv.DictReader(io.StringIO(get("-n", system, "exec", "deployment/celln-review-postgres-495", "-c", "postgres", "--", "psql", "-U", "celln_review", "-d", "celln_review", "--csv", "-c", sql))))
    runs = []
    for run in report["runs"]:
        meta, status = run["metadata"], run["status"]["cellnScoped"]
        receipt = json.loads(native("cat", "/state/scoped/status/" + status["receiverId"].removeprefix("sha256:")))
        for key, expected in [("id", status["receiverId"]), ("owner", status["owner"]), ("receiptDigest", status["receiptDigest"]), ("cellId", status["cellId"]), ("cleanupConfirmed", True), ("phase", "Succeeded")]:
            if receipt.get(key) != expected:
                raise ValueError("native tombstone mismatch: " + key)
        rows = [row for row in ledger if row["run_uid"] == meta["uid"]]
        model = "-model-" in meta["name"]
        if model:
            if len(rows) != 1 or any(rows[0][k] != v for k,v in {"reserved_requests":"2", "reserved_output_tokens":"1024", "observed_output_tokens":"16", "closed":"t"}.items()):
                raise ValueError("unexpected deterministic model ledger")
        elif rows:
            raise ValueError("direct run unexpectedly registered a model budget")
        remaining = json.loads(get("-n", meta["namespace"], "get", "agentrun", meta["name"], "--ignore-not-found", "-o", "json") or "null")
        if remaining is not None:
            raise ValueError("successful run remains after cleanup")
        runs.append({"namespace":meta["namespace"], "name":meta["name"], "uid":meta["uid"], "native":{k:receipt[k] for k in ["id","owner","phase","receiptDigest","cellId","cleanupConfirmed"]}, "ledger":rows, "runAbsentAfterCleanup":True})
    for namespace in {run["metadata"]["namespace"] for run in report["runs"]}:
        for kind in ["serviceaccounts", "roles", "rolebindings", "pods"]:
            remaining = json.loads(get("-n", namespace, "get", kind, "-o", "json"))["items"]
            if any(report["epoch"] in item["metadata"]["name"] for item in remaining):
                raise ValueError("temporary runner resource remains: " + kind)
    inventory = native("/usr/local/bin/celln", "--root", "/state", "ps", "--json")
    doctor = [json.loads(x) for x in native("/usr/local/bin/celln", "--root", "/state", "doctor", "--json").splitlines() if x]
    if inventory.strip():
        raise ValueError("unexpected live native inventory; inspect instead of claiming teardown")
    pods = json.loads(get("get", "pods", "-A", "-o", "json"))["items"]
    images = [{"namespace":p["metadata"]["namespace"], "pod":p["metadata"]["name"], "containers":[{"name":c["name"],"image":c["image"],"imageID":c.get("imageID","")} for c in p["status"].get("containerStatuses",[])]} for p in pods]
    repo = Path(__file__).resolve().parents[3]
    sha = subprocess.check_output(["git","-C",str(repo),"rev-parse","HEAD"],text=True).strip()
    evidence = {"installedAcceptance":False,"scope":"single-host KVM / scripted one-tool provider / live Kubernetes RBAC / Cilium network isolation", "cluster":cluster,"sympoziumSHA":sha,"reportSHA256":hashlib.sha256(args.report.read_bytes()).hexdigest(),"checks":report["checks"],"runs":runs,"nativeInventoryEmpty":True,"doctor":doctor,"images":images,"limitations":["Not the normal Helm install or fresh namespaces after one platform install", "One tool, not the A02 two-tool plan; ledger is not an independent provider call counter", "No real-provider credentials used; scripted fixture only", "No browser, enduring three-turn/workspace, migration, concurrency/recovery, multi-host or complete A01-A12 evidence", "Only the supplied report epoch is certified; unrelated or historical runs are not assessed"]}
    fd = os.open(args.output, os.O_WRONLY|os.O_CREAT|os.O_EXCL, 0o600)
    with os.fdopen(fd,"w") as f:
        json.dump(evidence,f,indent=2)
    print(f"Verified {len(runs)} native tombstones, two closed model ledgers, empty native inventory; installedAcceptance=false")


if __name__ == "__main__":
    main()
