# Agent Substrate public API (vendored)

`ateapi/v1` is a verbatim copy of `api/ateapi/v1` from
[agent-substrate/substrate](https://github.com/agent-substrate/substrate) at
the tag in `PIN`: the proto and the Go code generated from it upstream.
Apache-2.0, see `LICENSE`.

It is copied, not imported, because Substrate's Go module needs k8s.io v0.37,
controller-runtime v0.24 and Go 1.27, and the generated API code needs only
grpc and protobuf. `internal/substrate` is the only package that may import it.

Never edit these files. To move to another release, run
`hack/vendor-substrate-api.sh <tag> <path to a substrate checkout>`;
`make substrate-api-check` (part of `make vet`) fails if a file no longer
matches `PIN`.
