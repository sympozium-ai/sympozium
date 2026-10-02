# Publication review and offline verification

This contribution publishes existing partial development qualification for #510
and #495. It does not close either issue. `installedAcceptance` remains false.

## Evidence provenance

The JSON evidence snapshots and dated runbooks are historical, not executions of
this publication branch. They are copied byte-for-byte from the reviewed local
qualification work. The original execution used Sympozium
`43f00691e720c1588d3705ed08b54a53bf1477c8` plus then-uncommitted qualification
helpers; see each snapshot's source/hash fields and the dated runbooks. Publication
starts from `1dff955421e2c20ba6d39e022803c7a51723d399`. New offline unit-test success
must not be interpreted as a new installed run or a replay with revised helpers.

The evidence exports were reviewed for credentials, private keys, bearer/JWT
values, raw request headers, Kubernetes Secret contents and private kubeconfigs.
Only the checked-in metadata, counters, native/ledger identity evidence and dated
summaries are published. References to local paths identify historical artifacts;
those private files and logs are not included. Fixture credentials are generated
locally and must never be published. A failed earlier run remains documented;
the clean-room collector caveat is preserved rather than rewriting evidence.

## Publication-branch verification

Run from the repository root, with no cluster access:

```sh
GOMAXPROCS=2 go test -mod=readonly -p 2 -race -count=1 ./test/integration/celln-installed-qualification
GOMAXPROCS=2 go vet -mod=readonly -p 2 ./test/integration/celln-installed-qualification
python3 -B -m unittest discover -s test/integration/celln-installed-qualification -p 'test_*.py' -v
```

These commands passed during publication on 2026-09-24 (three Python bootstrap
tests). Initial readonly compilation found genuinely missing indirect SPDY exec
dependencies. Only `github.com/moby/spdystream v0.5.0` and
`github.com/mxk/go-flowrate v0.0.0-20140419014527-cca7078d478f` and their checksums
were added to this worktree's module files, via the existing Kubernetes v0.35.1
packages. No dependency versions were upgraded or primary-worktree module files
copied. No chart, dispatcher, workflow, persistence or cluster changes are part
of this contribution.

Remaining release gaps and the mutating runner's operator preconditions are in
[README.md](README.md). Review the exact dedicated cluster and private state
before any execution; labels alone are not proof of safe isolation.
