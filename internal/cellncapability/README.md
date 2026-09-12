# Celln capability package

`internal/cellncapability` is the production Go implementation of the scoped JWS portion of #496/#499.

It consumes the signed conformance bundle from `test/fixtures/celln-authorisation/v1`, uses Ed25519 JWS with locally configured keys, separates execution and model audiences, binds receiver-supplied operation context, and redacts bearer material by default.

Policy resolution, Celln host admission, the model gateway, and durable budget accounting are deliberately implemented by later stack layers rather than hidden here.
