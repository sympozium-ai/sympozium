# Changelog

## [0.8.4](https://github.com/sympozium-ai/sympozium/compare/v0.8.3...v0.8.4) (2026-03-31)


### Bug Fixes

* strip directory prefix from CRD names when writing to temp dir ([1906327](https://github.com/sympozium-ai/sympozium/commit/1906327b3abd32dc887f5a09c98eada9e0fb09b6))

## [0.8.3](https://github.com/sympozium-ai/sympozium/compare/v0.8.2...v0.8.3) (2026-03-31)


### Bug Fixes

* add metrics.k8s.io RBAC to config/rbac/role.yaml for sympozium install ([0c1a51c](https://github.com/sympozium-ai/sympozium/commit/0c1a51c8d11354aa5e2df694e8557c120b474857))

## [0.8.2](https://github.com/sympozium-ai/sympozium/compare/v0.8.1...v0.8.2) (2026-03-31)


### Bug Fixes

* resolve remaining TypeScript index signature errors in yaml-panel ([8cea011](https://github.com/sympozium-ai/sympozium/commit/8cea0119064536a30ba8a1a15d119af73c9380a9))

## [0.8.1](https://github.com/sympozium-ai/sympozium/compare/v0.8.0...v0.8.1) (2026-03-31)


### Bug Fixes

* fail AgentRun when skill RBAC creation fails instead of silently continuing ([99ddb4d](https://github.com/sympozium-ai/sympozium/commit/99ddb4d698bedd758c7d5512e6da354dad5db754))
* resolve TypeScript index signature errors in yaml-panel ([4a576a1](https://github.com/sympozium-ai/sympozium/commit/4a576a1b8db3f77c7ee6cb610b08f212b3ab9cd0))

## [0.8.0](https://github.com/sympozium-ai/sympozium/compare/v0.7.0...v0.8.0) (2026-03-30)


### Features

* lifecycle hooks — preRun and postRun containers for agent runs ([a29a8c9](https://github.com/sympozium-ai/sympozium/commit/a29a8c99a67287f063f2b1398b9e499b57e51d35))
* lifecycle hooks — preRun and postRun containers for agent runs ([#67](https://github.com/sympozium-ai/sympozium/issues/67)) ([46250af](https://github.com/sympozium-ai/sympozium/commit/46250afb1e379378e0a82d1d450a811f0a2181dc))


### Bug Fixes

* update API key retrieval to use header instead of query parameter ([e320e8d](https://github.com/sympozium-ai/sympozium/commit/e320e8d8361107acf30af4d35b9df2cd866c0cda))
* update API key retrieval to use header instead of query parameter ([ba6281a](https://github.com/sympozium-ai/sympozium/commit/ba6281a546a18f2b42193c5203049b08eb4eb983))
* update RBAC rules to include metrics.k8s.io permissions for skill sidecars ([cad5b4a](https://github.com/sympozium-ai/sympozium/commit/cad5b4a7eef051efd239604e472be905b4d28d21))
* update RBAC rules to include metrics.k8s.io permissions for skill sidecars ([3f90317](https://github.com/sympozium-ai/sympozium/commit/3f90317d172cc8d43a0d37b952196f48b3f73fe5))

## [0.7.0](https://github.com/sympozium-ai/sympozium/compare/v0.6.1...v0.7.0) (2026-03-29)


### Features

* add apiKey support for provider models fetching ([369fab3](https://github.com/sympozium-ai/sympozium/commit/369fab35e02dd9a5effadb9ce68ccd39d14f6b0e))
* add apiKey support for provider models fetching ([fb4bb53](https://github.com/sympozium-ai/sympozium/commit/fb4bb53b302ff0e11b176e9dba2e19a8856d2295))


### Bug Fixes

* AgentRun status concurrency update ([87dbb22](https://github.com/sympozium-ai/sympozium/commit/87dbb2226b22de4106d7c7c90fb77101c4217f38))
* prevent apiserver image build timeout on multi-arch builds ([830329d](https://github.com/sympozium-ai/sympozium/commit/830329d94295f04a496594ff494100a9e48fd1e1)), closes [#60](https://github.com/sympozium-ai/sympozium/issues/60)

## [0.6.1](https://github.com/sympozium-ai/sympozium/compare/v0.6.0...v0.6.1) (2026-03-28)


### Bug Fixes

* chain release workflow from release-please via workflow_call ([22c9e1e](https://github.com/sympozium-ai/sympozium/commit/22c9e1e9a17a52907e6c3424855bc82ce1cfb5b1))

## [0.6.0](https://github.com/sympozium-ai/sympozium/compare/v0.5.8...v0.6.0) (2026-03-28)


### Features

* Add image pull secret propagation for agent run container ([51858a3](https://github.com/sympozium-ai/sympozium/commit/51858a3686d9a7593eaf20def93e77ad726825b6))
* Add image pull secret propagation for agentrun sidecar container ([d5f4852](https://github.com/sympozium-ai/sympozium/commit/d5f4852515320378b2a36a31a7ff3e6e083f0f9f))
* add RBAC permissions for metrics access on pods and nodes ([013b02e](https://github.com/sympozium-ai/sympozium/commit/013b02eede3918664eed3f0018d93e8d66782be8))
* add RBAC permissions for metrics access on pods and nodes ([d94ed79](https://github.com/sympozium-ai/sympozium/commit/d94ed79da573375e22186ebc8e6d5c264e56549d))
