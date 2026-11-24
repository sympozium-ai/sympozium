# Substrate backend: live re-run for the PR (2026-11-24)

Re-run of `hack/substrate-e2e` on the framework cluster against the code in
this branch (after the contract hardening and review fixes), with a runner
image rebuilt from the branch (`localhost:5055/sympozium/agent-runner@sha256:fe83aea11359526bb0c812ac6a52a53d6ae09ce7997f85ffde1df60b4c49af2a`).

The controller and the model gateway ran out of cluster, in one process,
against a local envtest API server, so the live Sympozium release was not
touched. Substrate is the install recorded in
[substrate-install-framework-2026-10-30.md](substrate-install-framework-2026-10-30.md)
(`v1.0.0-rc.1` server; client built from the vendored `v1.0.1` API).
Stub provider that answers only the Agent's key; the clear-text hop was
acknowledged with `SUBSTRATE_ALLOW_PLAINTEXT_GATEWAY_HOP=true`.

| Batch | Succeeded | Created → Succeeded p50 | p95 | Provider calls with the Agent's key / without | Raw |
|---|---|---|---|---|---|
| 20 sequential | 20 of 20 | 0.39 s | 0.46 s | 20 / 0 | [raw](substrate-controller-runs-2026-11-24.json) |
| 40, 10 in flight | 40 of 40 | 0.45 s | 2.96 s | 40 / 0 | [raw](substrate-controller-runs-parallel-2026-11-24.json) |

Every run's actor was confirmed deleted by the Control API
(`createdToActorReleasedMs` is set for all 60). The stub key does not appear
in any run's result or logs. The p95 tail in the parallel batch is worker
pool capacity (two workers), as in the spike.
