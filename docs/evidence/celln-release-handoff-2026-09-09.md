# Native release handoff — 2026-09-09

Release pair: Celln v0.5.8 and Sympozium v0.10.57. The artifact dependency is
recorded in `images/celln-parent-controller/celln-release.json`; the host package
also records both sources and checksums. Publication/install checks are recorded
on PR #470 rather than inferred from a tag alone.

## Host service and certificate handoff

Framework's existing state roots, ownership ledger, run identities and model
credential locations were preserved. The native owner, native TLS edge, one-shot
router and execution TLS edge are now enabled for system startup; the existing
one-shot dispatcher was already enabled. All five were active after setup.
The node was **not rebooted** as part of this handoff, and live-context recovery
after a reboot is not supported or claimed.

The short-lived qualification CA certificate was backed up privately and renewed
using the same protected CA key for ten years. Public trust was distributed to
the existing native configuration Secret and one-shot client CA ConfigMap.
Neither bearer tokens nor model credentials were changed in this step.

The leaf is valid from 2026-09-09 11:53:58 UTC to 2026-12-08 11:53:58 UTC.
`sympozium-celln-tls-renew.timer` is enabled and checks daily. Renewal is triggered
below 30 days, uses a 90-day leaf, and restarts only TLS proxies. Unit tests with
real Ed25519 certificates verified leaf renewal, preserved private key, verified
IP identity, no-op repeat, exact proxy restart scope and near-expiry CA refusal.
CA expiry/rotation and failed timer alerts remain operator responsibilities.

## Observed idle service footprint

One `systemctl show ... -p MemoryCurrent` sample on framework, 2026-09-09
11:58:13 UTC, with no active test run:

| Service | cgroup MemoryCurrent (bytes) |
| --- | ---: |
| Native owner | 207851520 |
| Native TLS edge | 8421376 |
| One-shot dispatcher | 520687616 |
| Execution router | 495616 |
| Execution TLS edge | 3334144 |

These are cgroup accounting observations including resident/cached state after
qualification, not per-parent RAM costs, a cold baseline, capacity guarantees or
latency benchmarks. The earlier installed one-shot took 14 seconds including
forge/model work; that is not a warm-cell spawn measurement. See the individual
regression and guest proof records for their exact scope.
