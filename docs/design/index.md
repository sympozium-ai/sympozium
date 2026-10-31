# Design Notes & History

Design records behind Sympozium's features, kept for maintainers and for
anyone who wants to know *why* something works the way it does. They are not
user guides: for how things work today, start with
[Architecture](../architecture.md) and the Concepts pages.

## Design documents and decisions

- [ExecutionBackend seam: pluggable execution under the coordination layer](execution-backend-seam.md)
- [Celln: single execution plane](celln-single-execution-plane.md)
- [ADR: Harness identity, Celln placement and approved tool lending](celln-runtime-tool-contract.md)
- [Celln namespace authorisation and cross-repository conformance contract](celln-namespace-authorisation.md)
- [Celln platform policy resolution](celln-policy-resolution.md)
- [Exact-turn cancellation](celln-turn-cancellation.md)
- [Celln Harness starter toolbox — release requirements](celln-starter-toolbox.md)
- [Epic: enduring AgentRuns with persistent Harness parents and disposable turn cells](enduring-agentrun-celln-epic.md)
- [Persistent AgentHarness sessions](persistent-harness-sessions.md)
- [Design: AgentRun-level dollar cost estimation](cost-estimation.md)
- [Design: accelerator energy collectors](energy-collector.md)
- [Roadmap: Evolving Ensemble into a Multi-Agent Coordination System](ensemble-coordination-roadmap.md)

## Qualification and implementation records

Checkpoints from the Celln enduring-run and tenancy epics (#464, #495).

- [Parent/turn implementation inventory — epic #464](celln-parent-implementation-notes.md)
- [Model gateway live-API checkpoint (#502)](celln-gateway-live-epoch.md)
- [Celln tenancy component security suite (#509)](celln-tenancy-component-suite.md)
- [Celln release evidence validation (#510)](celln-tenancy-evidence-validation.md)
- [Framework: installed scoped Celln review 495](celln-framework-installed-review.md)
- [Framework manual acceptance target — epic #495](celln-framework-manual-acceptance.md)
- [Framework workspace: real-provider product walkthrough](celln-workspace-installed-review.md)

## Celln milestone records

Step-by-step records from the September 2026 Celln build-out: issuance,
catalogue, router and early harness milestones that the fleet has since
superseded. Each page carries a historical note.

- [Sympozium — Simplified Architecture](history/architecture-simplified.md)
- [Catalogue controller bridge — integration in progress](history/celln-catalogue-controller-bridge.md)
- [Durable catalogue issuance from the operator CLI](history/celln-durable-issuance-cli.md)
- [Enduring Celln run contract (development)](history/celln-enduring-run.md)
- [Isolated Celln framework manual review](history/celln-framework-manual-review.md)
- [Harness + Celln selection UX: administrator-assisted one-shot delivery](history/celln-harness-selection-ux.md)
- [Local Harness + Celln hands-on session](history/celln-interactive-session.md)
- [Verified remote issuer client](history/celln-issuer-client.md)
- [Authenticated Celln host issuer service](history/celln-issuer-service.md)
- [Installing the host issuer under systemd](history/celln-issuer-systemd.md)
- [Native JSON Harness in Celln: explicit binding](history/celln-json-harness.md)
- [Live catalogue-backed Harness proof](history/celln-live-catalogue-proof.md)
- [Catalogue-backed local host issuance](history/celln-local-issuance.md)
- [One-shot Harness-in-Celln MLP acceptance index](history/celln-mlp-acceptance.md)
- [One-shot borrowed-tool handoff](history/celln-mlp-borrowed-tools.md)
- [One-shot Harness-in-Celln: MLP installation checklist](history/celln-mlp-installation.md)
- [Isolated Kind pod-to-host proof: network boundary](history/celln-mlp-kind-network.md)
- [One-shot Harness-in-Celln troubleshooting](history/celln-mlp-troubleshooting.md)
- [Independent Celln model authority](history/celln-model-authority.md)
- [Local persistent-parent hands-on session](history/celln-parent-hands-on.md)
- [Native Celln parent-only controller](history/celln-parent-only-controller.md)
- [Celln tool permission preview](history/celln-permission-preview.md)
- [Experimental in-cell reference Harness binding](history/celln-reference-harness.md)
- [Automatic issuance for registered catalogue compositions](history/celln-registered-issuance.md)
- [Experimental Celln router deployment](history/celln-router-deployment.md)
- [Frozen-route execution transport](history/celln-router-execution-client.md)
- [Verified router prewarm client](history/celln-router-prewarm-client.md)
- [Celln runtime profile: metadata and independent readiness](history/celln-runtime-profile.md)
- [Celln tenancy migration: inventory and stop boundaries (#508)](history/celln-tenancy-migration.md)
- [Tool authority resolution core](history/celln-tool-authority.md)
- [Experimental Celln executable-tool catalogue metadata](history/celln-tool-catalogue.md)
- [Operator review and verified catalogue publication](history/celln-tool-review.md)

## Evidence

Recorded qualification runs. JSON evidence files sit alongside these in
`docs/evidence/`.

- [API startup with disabled/unavailable NATS — 2026-09-07](../evidence/apiserver-nats-startup-2026-09-07.md)
- [Deployed Celln capability discovery — 2026-09-07](../evidence/celln-capability-discovery-2026-09-07.md)
- [Celln fleet on multi-node Kind — 2026-09-14](../evidence/celln-fleet-kind-2026-09-14.md)
- [Framework native installation qualification — 2026-09-09](../evidence/celln-framework-installation-2026-09-09.md)
- [Framework router migration and cache regression qualification](../evidence/celln-framework-router-migration-2026-09-09.md)
- [Native release handoff — 2026-09-09](../evidence/celln-release-handoff-2026-09-09.md)
- [Native Celln starter system acceptance — 2026-09-09](../evidence/celln-starter-system-2026-09-09.md)

## Release notes

- [Sympozium 0.10.57 / Celln 0.5.8 — native Harness MLP](../releases/0.10.57-native-celln.md)

See also the [original design document](../design.md) (February 2026) and
the [CHANGELOG](https://github.com/sympozium-ai/sympozium/blob/main/CHANGELOG.md).
