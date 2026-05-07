# Changelog

## [0.10.20](https://github.com/sympozium-ai/sympozium/compare/v0.10.19...v0.10.20) (2026-05-07)


### Features

* topology dagre layout, synthetic membrane page, and UX improvements ([4cef6a2](https://github.com/sympozium-ai/sympozium/commit/4cef6a27b4cf6c01ffd89d7a9659243cf12bc94b))


### Bug Fixes

* sort runs list by creation date descending (fixes [#151](https://github.com/sympozium-ai/sympozium/issues/151)) ([bed055c](https://github.com/sympozium-ai/sympozium/commit/bed055c97af6abdff50edb8e93e2bf14bd164fca))

## [0.10.19](https://github.com/sympozium-ai/sympozium/compare/v0.10.18...v0.10.19) (2026-05-06)


### Features

* add envtest-based system tests for API server + controllers ([2344132](https://github.com/sympozium-ai/sympozium/commit/2344132a7483162e66fb6f5deea341ff8e39d017))
* channel pod CSI compatibility and dedicated service account ([1aa9a99](https://github.com/sympozium-ai/sympozium/commit/1aa9a992d6ca92ec2317c7d30dc2ea12ec27dafc))
* envtest-based system tests + Cypress fixes ([e173d95](https://github.com/sympozium-ai/sympozium/commit/e173d95afc89f193ccab21eaed7ed2b638d10022))
* stimulus node support in builder, unified canvas primitives, and UX fixes ([#162](https://github.com/sympozium-ai/sympozium/issues/162)) ([a57c8f1](https://github.com/sympozium-ai/sympozium/commit/a57c8f1c1ff7d41dcde2bb34ae0c84bf5ce79473))


### Bug Fixes

* add build tag to system tests so go test ./... skips them ([50052f0](https://github.com/sympozium-ai/sympozium/commit/50052f0d10ea250ec7e4984b28db97b98a00347c))
* propagate skill changes to existing Agents on ensemble update ([2a498c7](https://github.com/sympozium-ai/sympozium/commit/2a498c733bf10b5494572e850410b7c1339983b7))
* resolve flaky Cypress tests for run-delete and run-notifications ([74bab5a](https://github.com/sympozium-ai/sympozium/commit/74bab5a59cca869862facb1bd9e62edb9fbbcc71))

## [0.10.18](https://github.com/sympozium-ai/sympozium/compare/v0.10.17...v0.10.18) (2026-05-05)


### Features

* add Stimulus node type for auto-triggered workflow prompts ([59fc3be](https://github.com/sympozium-ai/sympozium/commit/59fc3be965733570e91da4e6aa2b3fb06ccf7fd3))

## [0.10.17](https://github.com/sympozium-ai/sympozium/compare/v0.10.16...v0.10.17) (2026-05-03)


### Features

* **cypress:** parameterize test model via CYPRESS_TEST_MODEL env var ([b4f68ea](https://github.com/sympozium-ai/sympozium/commit/b4f68ea8dd5ba0ad6eef18476d5630d4d0c486dc))
* **cypress:** parameterize test model via CYPRESS_TEST_MODEL env var ([af6310b](https://github.com/sympozium-ai/sympozium/commit/af6310b0f3ebfe6d361e75b6242bed6572546e53))


### Bug Fixes

* restrict WhatsApp channel to self-chat messages only ([3425eb8](https://github.com/sympozium-ai/sympozium/commit/3425eb80290f95ce229e91850caef1f1db4e8e6b))
* restrict WhatsApp channel to self-chat messages only ([6af4dca](https://github.com/sympozium-ai/sympozium/commit/6af4dcaad6f9e2372e917c4f95e3dd952c706c3e)), closes [#138](https://github.com/sympozium-ai/sympozium/issues/138)
* **web-proxy:** close subscribe-before-find race and skip terminal runs ([71786b7](https://github.com/sympozium-ai/sympozium/commit/71786b736dc6ec8ef93c0ecaf31d04f5a2771a45))
* **web-proxy:** close subscribe-before-find race and skip terminal runs ([77c1267](https://github.com/sympozium-ai/sympozium/commit/77c12673dad1d21d063418a527c88ae1c85018b7))
* **web-proxy:** dedupe retried chat requests ([bec7af2](https://github.com/sympozium-ai/sympozium/commit/bec7af286bd761016659c838af7d1819172bc48b))
* **web-proxy:** dedupe retried chat requests ([d4233f3](https://github.com/sympozium-ai/sympozium/commit/d4233f3f4fc7c8153e1b7f2d9737d36c7340b988))
* **web:** prevent gateway toggle from disconnecting UI ([9ededbd](https://github.com/sympozium-ai/sympozium/commit/9ededbd830eb7c409e4340a40b222fd1c7651de4))
* **web:** prevent gateway toggle from disconnecting UI ([3ef4772](https://github.com/sympozium-ai/sympozium/commit/3ef4772500b15a0149be8c1242ff48154ceb8ee0))

## [0.10.16](https://github.com/sympozium-ai/sympozium/compare/v0.10.15...v0.10.16) (2026-05-01)


### Features

* auto-inject delegation/supervision context from ensemble relationships ([e38e93e](https://github.com/sympozium-ai/sympozium/commit/e38e93ef6f930baf3149c4765a14644a1307154f))


### Bug Fixes

* canary first run never triggers after duplicate-prevention change ([0bbf126](https://github.com/sympozium-ai/sympozium/commit/0bbf12614d18fb260acb498514d204f34b0f1126))
* canary first run never triggers after duplicate-prevention change ([2e1caeb](https://github.com/sympozium-ai/sympozium/commit/2e1caeb2e0fbdf33b07463f059a5e6f90ec2a2ac))

## [0.10.15](https://github.com/sympozium-ai/sympozium/compare/v0.10.14...v0.10.15) (2026-05-01)


### Features

* expand default MCP server catalog ([ab27fac](https://github.com/sympozium-ai/sympozium/commit/ab27fac64b0b1ebdc6072de351c511439d8869a8))
* expand default MCP server catalog with grafana, kubernetes, argocd, and postgres ([b620dbf](https://github.com/sympozium-ai/sympozium/commit/b620dbfb5aed5a2767bd4d50917e4f4a19ec897f))


### Bug Fixes

* correct MCP server configs after local testing ([6d56e57](https://github.com/sympozium-ai/sympozium/commit/6d56e57d17d23cc5db1505cd90299ed1409f2a84))
* default MCP server catalog to disabled (opt-in) ([d164dc0](https://github.com/sympozium-ai/sympozium/commit/d164dc01fa7488daf8beac7c7f31d43a839ca5fc))
* prevent duplicate canary runs on first schedule trigger ([1f5e286](https://github.com/sympozium-ai/sympozium/commit/1f5e2864ecc5bd421ddfc0fa73f0533e963c7f55))
* prevent duplicate canary runs on first schedule trigger ([1428d68](https://github.com/sympozium-ai/sympozium/commit/1428d68df148a004e60e9e7e47d11902a094fea6))

## [0.10.14](https://github.com/sympozium-ai/sympozium/compare/v0.10.13...v0.10.14) (2026-05-01)


### Features

* add structured health check matrix to canary UI ([73d54c1](https://github.com/sympozium-ai/sympozium/commit/73d54c1ab07d5d74af2a9ecd0ef68ad28af5df74))
* replace LLM-based canary with deterministic health checks ([2e25fd1](https://github.com/sympozium-ai/sympozium/commit/2e25fd1a98481362ba382d4240cecf2069533d9b))


### Bug Fixes

* canary NetworkPolicy, RBAC, provider resolution, and node-probe routing ([5be1db0](https://github.com/sympozium-ai/sympozium/commit/5be1db0031bcdf19be09521036740ca5861414de))
* hide system canary from ensembles list ([f7c051c](https://github.com/sympozium-ai/sympozium/commit/f7c051cf84e607a18bd350b54ec922c34467f824))
* tighten canary system prompt to prevent command retries ([c226a02](https://github.com/sympozium-ai/sympozium/commit/c226a02fa28a71bd81780c005a07eed2fca3c7c3))

## [0.10.13](https://github.com/sympozium-ai/sympozium/compare/v0.10.12...v0.10.13) (2026-04-30)


### Bug Fixes

* include SympoziumConfig in CLI uninstall resource cleanup ([4d296e4](https://github.com/sympozium-ai/sympozium/commit/4d296e4ea46b60c55d06d184ccb2cad0160b65a2))
* prevent canary agent from looping on empty memory ([23e9088](https://github.com/sympozium-ai/sympozium/commit/23e908830326d196bf37ce77802bdbbd2ab8eec3))

## [0.10.12](https://github.com/sympozium-ai/sympozium/compare/v0.10.11...v0.10.12) (2026-04-30)


### Features

* add System Canary — built-in synthetic health testing ensemble ([fef2742](https://github.com/sympozium-ai/sympozium/commit/fef27420c9bff4c4492c14c0df4b71cdf1fdb904))


### Bug Fixes

* render markdown in feed task messages ([7275510](https://github.com/sympozium-ai/sympozium/commit/72755103e0b679330a7576f378cea4a02eb0e22d))
* unify canary connection test and fix agent-run NetworkPolicy ([3909012](https://github.com/sympozium-ai/sympozium/commit/39090124376dcd2b94a481cfb2e87e8aa6406dd6))

## [0.10.11](https://github.com/sympozium-ai/sympozium/compare/v0.10.10...v0.10.11) (2026-04-29)


### Features

* enforce ExposeTags and MaxTokensPerRun membrane fields ([b6aa66c](https://github.com/sympozium-ai/sympozium/commit/b6aa66c1b2054169fbe5608163ae5aa50b68b078))


### Bug Fixes

* add missing nodes RBAC for apiserver — restores topology and cluster status ([58ad746](https://github.com/sympozium-ai/sympozium/commit/58ad746c8fba7d1d18365ee023d5492372acacd7))

## [0.10.10](https://github.com/sympozium-ai/sympozium/compare/v0.10.9...v0.10.10) (2026-04-29)


### Bug Fixes

* run make generate for new policy and model types ([2449644](https://github.com/sympozium-ai/sympozium/commit/244964411417572c54ca07f7ec2028a73c048842))

## [0.10.9](https://github.com/sympozium-ai/sympozium/compare/v0.10.8...v0.10.9) (2026-04-29)


### Bug Fixes

* security hardening for Model, AgentRun, and Ensemble features ([21fc58d](https://github.com/sympozium-ai/sympozium/commit/21fc58dc46b3ad0935f184afd90fd5359cd8e5eb))

## [0.10.8](https://github.com/sympozium-ai/sympozium/compare/v0.10.7...v0.10.8) (2026-04-28)


### Features

* add ensemble delete button + auto-derive permeability from relationships ([93a8ec1](https://github.com/sympozium-ai/sympozium/commit/93a8ec1c3496742275365ee2f410de7ac488e08a))
* add synthetic membrane layer for shared workflow memory ([5a30192](https://github.com/sympozium-ai/sympozium/commit/5a3019269a3ee9f7e73e5eab6cc30755b52f552d))
* synthetic membrane layer for shared workflow memory ([a582317](https://github.com/sympozium-ai/sympozium/commit/a5823176a3e03bd80489ea9542c0c78b2c0b4154))


### Bug Fixes

* update expected default Ensemble count from 5 to 6 ([e2aedf3](https://github.com/sympozium-ai/sympozium/commit/e2aedf3d5bf23f1ccddf6f9191338ad005d929bb))

## [0.10.7](https://github.com/sympozium-ai/sympozium/compare/v0.10.6...v0.10.7) (2026-04-28)


### Bug Fixes

* add missing DryRun field and supporting changes omitted from dc2c7a6 ([7f0a4aa](https://github.com/sympozium-ai/sympozium/commit/7f0a4aaf9f17ee46f408a839d512c18590833098))

## [0.10.6](https://github.com/sympozium-ai/sympozium/compare/v0.10.5...v0.10.6) (2026-04-27)


### Features

* add topology page to demo walkthrough recording ([ae6d8fc](https://github.com/sympozium-ai/sympozium/commit/ae6d8fc88d4ecdfa81dafc2f044fbdb2a99135f0))
* implement blocking delegation between ensemble personas ([dc2c7a6](https://github.com/sympozium-ai/sympozium/commit/dc2c7a6cba1cced245ae3390d618e2352b2fd6c7))

## [0.10.5](https://github.com/sympozium-ai/sympozium/compare/v0.10.4...v0.10.5) (2026-04-27)


### Bug Fixes

* topology page TypeScript build errors ([8a9b712](https://github.com/sympozium-ai/sympozium/commit/8a9b712e55827c975e653c9d3f4a3779ea5684af))

## [0.10.4](https://github.com/sympozium-ai/sympozium/compare/v0.10.3...v0.10.4) (2026-04-27)


### Features

* multi-provider inference (vLLM, TGI) and cluster topology page ([c434df4](https://github.com/sympozium-ai/sympozium/commit/c434df48788878d3dee87224cde2345a3cca66a7))


### Bug Fixes

* gofmt formatting for inference backend files ([8d837bd](https://github.com/sympozium-ai/sympozium/commit/8d837bdd332b68cd544c8ef45962237cf5237710))
* remove redundant lock button from topology zoom controls ([2e07dc9](https://github.com/sympozium-ai/sympozium/commit/2e07dc9491f8c4086c2113536eee4d41eea32136))

## [0.10.3](https://github.com/sympozium-ai/sympozium/compare/v0.10.2...v0.10.3) (2026-04-26)


### Features

* add automated demo walkthrough recording for README GIF ([0945630](https://github.com/sympozium-ai/sympozium/commit/09456301cb845e8720abb64ce59b833fa87ea181))


### Bug Fixes

* crop gray borders from demo GIF recording ([c300672](https://github.com/sympozium-ai/sympozium/commit/c3006725a6b23bba0ca9200e6404324151a11e74))

## [0.10.2](https://github.com/sympozium-ai/sympozium/compare/v0.10.1...v0.10.2) (2026-04-26)


### Features

* add YAML export button to ensemble detail page ([f970d44](https://github.com/sympozium-ai/sympozium/commit/f970d448476a159a2d6d076eff42cafeb6f43dd7))


### Bug Fixes

* remove duplicate YamlButton import in ensemble-detail ([a82a493](https://github.com/sympozium-ai/sympozium/commit/a82a4931072bef5f35a088ee26542819d2b8c41a))

## [0.10.1](https://github.com/sympozium-ai/sympozium/compare/v0.10.0...v0.10.1) (2026-04-26)


### Bug Fixes

* infer local model provider from agent model fields ([6393251](https://github.com/sympozium-ai/sympozium/commit/6393251ff0d8f48ab65fa3361b4a29bab3607566))

## [0.10.0](https://github.com/sympozium-ai/sympozium/compare/v0.9.5...v0.10.0) (2026-04-26)


### ⚠ BREAKING CHANGES

* This is a full ontology rename that affects CRDs, API routes, Go types, controllers, frontend, Helm charts, docs, and tests.

### Features

* add Concepts modal explaining Sympozium ontology ([9d4bef3](https://github.com/sympozium-ai/sympozium/commit/9d4bef347b1b27b6c3446b254117c581b9c85f11))
* add Local Model as provider option in ensemble builder ([83f032a](https://github.com/sympozium-ai/sympozium/commit/83f032acada1e360dc57538d7a662b8c70e37c9d))
* Add Provider button on builder and detail workflow canvases ([a962f69](https://github.com/sympozium-ai/sympozium/commit/a962f69df181244fe9a6b8f71e3317c68c894a7e))
* add workflows to all default ensembles ([6ad01b9](https://github.com/sympozium-ai/sympozium/commit/6ad01b9be9a4c7a23658c120a47269073bdf0ad5))
* provider nodes on canvas + per-persona provider overrides ([4bf004a](https://github.com/sympozium-ai/sympozium/commit/4bf004aaf435c44fb7d4e44270e26898a04f56b9))
* provider nodes on dashboard canvas, fix provider-to-agent wiring ([7350791](https://github.com/sympozium-ai/sympozium/commit/73507911d4450d548e8fd8fa494ee61bc6384942))
* rename Instance→Agent, Persona→AgentConfig across entire codebase ([df230ee](https://github.com/sympozium-ai/sympozium/commit/df230eeab513085d4fd713702efd5cfefda41766))


### Bug Fixes

* generate human-readable random agent names instead of persona-1 ([6c53dd3](https://github.com/sympozium-ai/sympozium/commit/6c53dd352a15d67b0f0d156c9da4cc21bad41652))
* model detail page namespace resolution ([894f808](https://github.com/sympozium-ai/sympozium/commit/894f808e533d4d2b8f40de5d411815016b506153))
* prevent canvas crash when model data hasn't loaded yet ([ddb5410](https://github.com/sympozium-ai/sympozium/commit/ddb541073fc81afe65901c58fd7595078ea5b3f2))
* rename Add Persona→Add Agent, hide GPT models for local model, sort skills ([827f695](https://github.com/sympozium-ai/sympozium/commit/827f6953992244abf28e6987ee5cc49c8dda8127))
* rename personas→agentConfigs in default ensemble YAML files ([95c4453](https://github.com/sympozium-ai/sympozium/commit/95c445389d60fd623e2548e52daa39a7ba761c94))
* replace all remaining Instance→Agent in user-facing UI strings ([b3ceb3d](https://github.com/sympozium-ai/sympozium/commit/b3ceb3d1ed2b3c22ef21eba32c1c205dfac271a2))
* resolve Docker build TS errors for provider nodes ([91147fb](https://github.com/sympozium-ai/sympozium/commit/91147fbb9dfe589c24aa9dacc64a8270879d4545))
* resolve Docker build TS errors from Instance→Agent rename ([926b5d7](https://github.com/sympozium-ai/sympozium/commit/926b5d7c5115d3ced126d2fe6f25be1d5223ddfc))
* wire modelRef into ensemble creation and fix persona default model ([85a504a](https://github.com/sympozium-ai/sympozium/commit/85a504a2f25da1e999ff58c4d9283a4251db9c2e))
* wiring a local model provider updates agent config panel and node ([05b8f28](https://github.com/sympozium-ai/sympozium/commit/05b8f28bc1788536acbc3492d6346e7a5f8c0d25))

## [0.9.5](https://github.com/sympozium-ai/sympozium/compare/v0.9.4...v0.9.5) (2026-04-25)


### Features

* show local model node on ensemble workflow canvas ([13b08e5](https://github.com/sympozium-ai/sympozium/commit/13b08e5e2f28afd57f7097440d5ba01cc265957a))
* show model node on global ensemble canvas ([3f00fef](https://github.com/sympozium-ai/sympozium/commit/3f00fef205b22c188c55346f6ea07daad63f03f7))


### Bug Fixes

* resolve TypeScript errors in ensemble canvas model node types ([b0fa56f](https://github.com/sympozium-ai/sympozium/commit/b0fa56f531990e3ceb6f97417538a4443563a543))
* skip global namespace filter on model API endpoints ([454c872](https://github.com/sympozium-ai/sympozium/commit/454c8720d3d3e9140ba75c70fdc5cacaab81fdb5))
* use namespace dropdown in model deploy dialog ([e2eef80](https://github.com/sympozium-ai/sympozium/commit/e2eef80ef844f4a574c3605c7e6014018b3a4810))

## [0.9.4](https://github.com/sympozium-ai/sympozium/compare/v0.9.3...v0.9.4) (2026-04-25)


### Features

* auto node placement via llmfit, namespace-aware models, and ModelPolicy groundwork ([2c13faf](https://github.com/sympozium-ai/sympozium/commit/2c13faf67c0139e6bd44b839cc736b4af8245c07))

## [0.9.3](https://github.com/sympozium-ai/sympozium/compare/v0.9.2...v0.9.3) (2026-04-25)


### Features

* declarative local model inference via Model CRD ([1a6da42](https://github.com/sympozium-ai/sympozium/commit/1a6da42cb691fa0f4569e3fe8cb450f5408f4494))
* declarative local model inference via Model CRD ([4095ea8](https://github.com/sympozium-ai/sympozium/commit/4095ea88ef85f3f32f2a4b7bb809907f648c04a8))


### Bug Fixes

* prevent UI token mismatch after helm upgrade ([32bd78c](https://github.com/sympozium-ai/sympozium/commit/32bd78c8532efd0c4fdd1df49b7b432c31e1b928))
* prevent UI token mismatch after helm upgrade ([dac1e87](https://github.com/sympozium-ai/sympozium/commit/dac1e8783bcc8fca0122f470b1d3800587bb5e7d)), closes [#113](https://github.com/sympozium-ai/sympozium/issues/113)

## [0.9.2](https://github.com/sympozium-ai/sympozium/compare/v0.9.1...v0.9.2) (2026-04-22)


### Bug Fixes

* per-persona Discord channel routing and truncated run results ([9407420](https://github.com/sympozium-ai/sympozium/commit/9407420c06c64b3289800c83d804a8f62f4acd31))
* per-persona Discord channel routing and truncated run results ([822f9ab](https://github.com/sympozium-ai/sympozium/commit/822f9ab02891673e59cbe2b45d2c6d2071d269fd)), closes [#106](https://github.com/sympozium-ai/sympozium/issues/106) [#107](https://github.com/sympozium-ai/sympozium/issues/107)

## [0.9.1](https://github.com/sympozium-ai/sympozium/compare/v0.9.0...v0.9.1) (2026-04-19)


### Features

* add node probe discovery to ensemble builder provider setup ([0576c7e](https://github.com/sympozium-ai/sympozium/commit/0576c7e44191d39e15c2ea7f5ef92a525d80724a))
* add workflow relationships to developer-team ensemble ([49d8e85](https://github.com/sympozium-ai/sympozium/commit/49d8e851d14583d40ed8e8f7f42c77869cd0f4ad))
* auto-detect node probe providers and allow changing ensemble provider ([e79310f](https://github.com/sympozium-ai/sympozium/commit/e79310f0950c9d2e740f37dddc70b4ba2f36f8fb))


### Bug Fixes

* heredoc syntax error in ux-test preflight script ([abd0f5d](https://github.com/sympozium-ai/sympozium/commit/abd0f5d5cad7eff3e3983b0ec1603b547e6cc8f6))
* stabilize workflow canvas layout across data refreshes ([b83378a](https://github.com/sympozium-ai/sympozium/commit/b83378a1ff88c4546781598fc4bd52e65dd22ce9))

## [0.9.0](https://github.com/sympozium-ai/sympozium/compare/v0.8.28...v0.9.0) (2026-04-19)


### ⚠ BREAKING CHANGES

* Ensemble CRD replaces PersonaPack (see commit 432355b).
* The PersonaPack CRD has been renamed to Ensemble. All API endpoints, labels, controllers, and UI references updated.

### Features

* add shared workflow memory for cross-persona knowledge sharing ([3a163dc](https://github.com/sympozium-ai/sympozium/commit/3a163dc5656e9cce1fa8cf5b2cd775e4f91f33a9))
* implement sequential workflow trigger in controller ([c5b9e45](https://github.com/sympozium-ai/sympozium/commit/c5b9e456f78261a35043e45e672342dc3eeac0f0))
* real-time workflow canvas updates via WebSocket ([e3fe61f](https://github.com/sympozium-ai/sympozium/commit/e3fe61f2cfa3ef2d5e6ddaf6e5e215e1399afd35))
* rename PersonaPack to Ensemble + canvas-first builder ([432355b](https://github.com/sympozium-ai/sympozium/commit/432355bca86ddf8b78d4ac6ec5be708613634bcd))


### Bug Fixes

* resolve all Cypress TypeScript errors ([008266e](https://github.com/sympozium-ai/sympozium/commit/008266efbcec1f39e4929c89c3bf79cb581e3d23))
* update all stale Persona Pack UI strings to Ensemble ([12fdaec](https://github.com/sympozium-ai/sympozium/commit/12fdaec4c6f73cc9f9febe87bd9d3ed61644f3ed))

## [0.8.28](https://github.com/sympozium-ai/sympozium/compare/v0.8.27...v0.8.28) (2026-04-16)


### Features

* promote Team Canvas to prominent dashboard position ([958600a](https://github.com/sympozium-ai/sympozium/commit/958600a3e7cd7d3f506f62607a6e97ce466e965a))

## [0.8.27](https://github.com/sympozium-ai/sympozium/compare/v0.8.26...v0.8.27) (2026-04-16)


### Features

* add persona relationships schema and workflow canvas ([ace2bcf](https://github.com/sympozium-ai/sympozium/commit/ace2bcf788612c25e28d0e3e8c582f973d80c90f))
* add research-team PersonaPack with relationships and Cypress tests ([9357e0a](https://github.com/sympozium-ai/sympozium/commit/9357e0a2ec3fd0ac354ccc80da5c7c3a79db9d43))
* AwaitingDelegate phase, Cypress workflow tests, hooks fix ([8fee27b](https://github.com/sympozium-ai/sympozium/commit/8fee27b9645729c6990d3471dd2240224f26c6c2))
* delegate_to_persona tool and dashboard team canvas widget ([5b25b59](https://github.com/sympozium-ai/sympozium/commit/5b25b596c956ea3896d14a5d8d64d81177b0db6b))
* global persona canvas and live run status highlighting ([5e69827](https://github.com/sympozium-ai/sympozium/commit/5e69827d36f4e7d9c053c29631ef4071e46833a3))
* interactive canvas editing and persona-targeted spawning ([c3af2ea](https://github.com/sympozium-ai/sympozium/commit/c3af2ea143186de52c9f99f6e499cf48a646a860))


### Bug Fixes

* canvas empty state — use controlled ReactFlow props for read-only canvases ([58697be](https://github.com/sympozium-ai/sympozium/commit/58697bef2f880488db35c81c82a7a0370fa69f71))

## [0.8.26](https://github.com/sympozium-ai/sympozium/compare/v0.8.25...v0.8.26) (2026-04-16)


### Features

* add Settings page with Agent Sandbox CRD install/uninstall, MCP server auth & defaults ([833bbdc](https://github.com/sympozium-ai/sympozium/commit/833bbdce455457252b7ffc7abf879b74a98a13cd))


### Bug Fixes

* validate instance name as RFC 1123 subdomain in wizard ([714a405](https://github.com/sympozium-ai/sympozium/commit/714a4059ebd356e434bdfe941ed68cf1ca2501e7))

## [0.8.25](https://github.com/sympozium-ai/sympozium/compare/v0.8.24...v0.8.25) (2026-04-12)


### Features

* add provider icons to wizard dropdown and llama-server docs ([25fca6d](https://github.com/sympozium-ai/sympozium/commit/25fca6dfddf43c18725d6e8ef4f0fa963c097ed3))

## [0.8.24](https://github.com/sympozium-ai/sympozium/compare/v0.8.23...v0.8.24) (2026-04-12)


### Features

* add llama-server as a first-class AI provider ([86ec4ae](https://github.com/sympozium-ai/sympozium/commit/86ec4ae6b202488ff5adfd012b9c790557d1a097))
* fmt code ([f6f61c3](https://github.com/sympozium-ai/sympozium/commit/f6f61c39e008fc489b2a5ad27ed1bb86295cc8f3))

## [0.8.23](https://github.com/sympozium-ai/sympozium/compare/v0.8.22...v0.8.23) (2026-04-11)


### Bug Fixes

* **install:** disable chart namespace template to avoid collision ([e0aae1c](https://github.com/sympozium-ai/sympozium/commit/e0aae1c3a54a95ee6bd5a8d0a2cf1c9c5d9b4b50))
* **install:** recover from failed releases and simplify ns creation ([4c84612](https://github.com/sympozium-ai/sympozium/commit/4c846129d61b99829ef7219c7dc1ed7c4edb6607))

## [0.8.22](https://github.com/sympozium-ai/sympozium/compare/v0.8.21...v0.8.22) (2026-04-10)


### Features

* fmt code ([fee9454](https://github.com/sympozium-ai/sympozium/commit/fee9454e5cf31cd8e4b8e7e067ba8271bb4ee036))

## [0.8.21](https://github.com/sympozium-ai/sympozium/compare/v0.8.20...v0.8.21) (2026-04-10)


### Features

* **gate:** add response gate hooks with manual approval flow ([0e5ad97](https://github.com/sympozium-ai/sympozium/commit/0e5ad9718826a2b0b776131890a6aad9dcaa5a49))

## [0.8.20](https://github.com/sympozium-ai/sympozium/compare/v0.8.19...v0.8.20) (2026-04-07)


### Features

* **web:** add run notifications, unseen watermark, and polling ([42bb00b](https://github.com/sympozium-ai/sympozium/commit/42bb00b9cceae427a0ce3a0c2b12895b94e5e6af))

## [0.8.19](https://github.com/sympozium-ai/sympozium/compare/v0.8.18...v0.8.19) (2026-04-07)


### Features

* **providers:** add Unsloth as a supported local LLM provider ([9c246c1](https://github.com/sympozium-ai/sympozium/commit/9c246c13ba8947b4fe026836e764786b43329126))
* **web:** improve sidebar hierarchy, breadcrumbs, and detail page UX ([0a622d1](https://github.com/sympozium-ai/sympozium/commit/0a622d176c0ee0ad536273d5eb61c277a5e778d1))


### Bug Fixes

* **personas:** harden platform-team prompts + propagate systemPrompt edits ([079986d](https://github.com/sympozium-ai/sympozium/commit/079986d5e8edc00cd85cf9ed4d715b36f85a589b))

## [0.8.18](https://github.com/sympozium-ai/sympozium/compare/v0.8.17...v0.8.18) (2026-04-05)


### Bug Fixes

* cascade-delete scheduled AgentRuns when their Schedule is removed ([eb1ad6a](https://github.com/sympozium-ai/sympozium/commit/eb1ad6af113686ae5b77c5d3b28c4ba9a913aabb))
* scheduler picks next free run-number suffix to avoid ghost runs ([205829a](https://github.com/sympozium-ai/sympozium/commit/205829a2c1525d2b2cf5fbdb09829b254790f601))

## [0.8.17](https://github.com/sympozium-ai/sympozium/compare/v0.8.16...v0.8.17) (2026-04-05)


### Features

* **makefile:** add ux-tests-serve target for running Cypress against sympozium serve ([e9c3202](https://github.com/sympozium-ai/sympozium/commit/e9c3202d98105eff3d1b7d6008b9b4f7cd7a4d2e))


### Bug Fixes

* prevent reconcile race from overriding Succeeded AgentRuns as Failed ([d681a33](https://github.com/sympozium-ai/sympozium/commit/d681a3359f1d64ec2d8755402c0abe3849d96e8a))

## [0.8.16](https://github.com/sympozium-ai/sympozium/compare/v0.8.15...v0.8.16) (2026-04-04)


### Features

* recover qwen-native tool_calls from reasoning_content ([f807de1](https://github.com/sympozium-ai/sympozium/commit/f807de172243672997d25c3cd311740b34396fcb))

## [0.8.15](https://github.com/sympozium-ai/sympozium/compare/v0.8.14...v0.8.15) (2026-04-04)


### Bug Fixes

* surface reasoning-model responses when terminal turn is empty ([045f7d7](https://github.com/sympozium-ai/sympozium/commit/045f7d74a2f95b5ebab7eba392c6d4441734368b))

## [0.8.14](https://github.com/sympozium-ai/sympozium/compare/v0.8.13...v0.8.14) (2026-04-04)


### Bug Fixes

* skip Helm CreateNamespace when sympozium-system already exists ([e40b157](https://github.com/sympozium-ai/sympozium/commit/e40b157a238de6b91cd8f0e0e18c771eb66e5a0d))

## [0.8.13](https://github.com/sympozium-ai/sympozium/compare/v0.8.12...v0.8.13) (2026-04-04)


### Miscellaneous Chores

* release 0.8.13 ([8a6fa7b](https://github.com/sympozium-ai/sympozium/commit/8a6fa7b870da36f0df6ab0bcccaeda6b1f04fec4))

## [0.8.12](https://github.com/sympozium-ai/sympozium/compare/v0.8.11...v0.8.12) (2026-04-04)


### Bug Fixes

* publish TopicAgentRunFailed from controller so web proxy unblocks on failure ([b98841f](https://github.com/sympozium-ai/sympozium/commit/b98841fe441a3c3f478640963c270fd7ed31dd85))

## [0.8.11](https://github.com/sympozium-ai/sympozium/compare/v0.8.10...v0.8.11) (2026-04-04)


### Features

* add Cypress UX tests for instance creation and persona packs ([2ffb502](https://github.com/sympozium-ai/sympozium/commit/2ffb5026b82b116ab027c09bed58be9b9a02e8f1))
* add Cypress UX tests for instance creation and persona packs ([55e5590](https://github.com/sympozium-ai/sympozium/commit/55e5590af21dbea24e594ec7437052cc89ded4dc))
* add tool-call circuit breaker and configurable run timeout ([b5a3b94](https://github.com/sympozium-ai/sympozium/commit/b5a3b94cefeb6c7cf68a1c6f90181a2f45f28344))
* expose run timeout in web UI and CLI TUI ([3bca472](https://github.com/sympozium-ai/sympozium/commit/3bca472642dcf85df6a4f6d0f242f2ed08e3553e))


### Bug Fixes

* resolve integration test hang and flaky secret-not-found error ([2fb431f](https://github.com/sympozium-ai/sympozium/commit/2fb431f99b42e14f6f123dbf6f62229ea3a06db0))
* use sentinel value for run timeout Select to avoid Radix crash ([1553b75](https://github.com/sympozium-ai/sympozium/commit/1553b75912c1ed4037bd622de09abeaed57f290d))

## [0.8.10](https://github.com/sympozium-ai/sympozium/compare/v0.8.9...v0.8.10) (2026-04-04)


### Bug Fixes

* remove conflicting namespace pre-creation in helm install ([9930ba4](https://github.com/sympozium-ai/sympozium/commit/9930ba4497989fa40d2461e9bef7039586c67aa0))

## [0.8.9](https://github.com/sympozium-ai/sympozium/compare/v0.8.8...v0.8.9) (2026-04-02)


### Bug Fixes

* auto-store task/response in memory server after each agent run ([8f475fb](https://github.com/sympozium-ai/sympozium/commit/8f475fbc2bf600ca7fad12394e7c417dd63e2509))
* guard stale Job-not-found reconcile during postRun transition ([8d2ff41](https://github.com/sympozium-ai/sympozium/commit/8d2ff41972acb551a9aabc13cc02c1807ca50560))

## [0.8.8](https://github.com/sympozium-ai/sympozium/compare/v0.8.7...v0.8.8) (2026-04-01)


### Features

* reworked memory implementation ([81fdd0c](https://github.com/sympozium-ai/sympozium/commit/81fdd0c83725dc068bc869f01b5d1af5c421c282))


### Bug Fixes

* add missing observability-mcp-team persona pack to Helm chart ([fc0105c](https://github.com/sympozium-ai/sympozium/commit/fc0105c0d243bb0adc58680e29a4827b7aad88bd))

## [0.8.7](https://github.com/sympozium-ai/sympozium/compare/v0.8.6...v0.8.7) (2026-03-31)


### Bug Fixes

* stop Helm template from overriding node-probe host auto-detection ([4f0e5f4](https://github.com/sympozium-ai/sympozium/commit/4f0e5f41217d5ec9bf165dda7796be0df3fd307d))

## [0.8.6](https://github.com/sympozium-ai/sympozium/compare/v0.8.5...v0.8.6) (2026-03-31)


### Bug Fixes

* create namespace before Helm config init to fix fresh installs ([e49fa50](https://github.com/sympozium-ai/sympozium/commit/e49fa50f26604688a1dcbba6a3d06543b0442ea8))

## [0.8.5](https://github.com/sympozium-ai/sympozium/compare/v0.8.4...v0.8.5) (2026-03-31)


### Bug Fixes

* remove explicit host from node-probe targets to restore auto-detection ([f91229a](https://github.com/sympozium-ai/sympozium/commit/f91229afa5ba5ad0674ba6c9b202932b2a869f3f))

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
