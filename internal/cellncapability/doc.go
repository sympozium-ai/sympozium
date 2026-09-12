// Package cellncapability issues and verifies the scoped, audience-bound
// Celln authorisation credentials defined by docs/design/celln-namespace-authorisation.md.
//
// It deliberately contains no Kubernetes policy resolver or model-budget
// database. Those are separate enforcement boundaries. This package turns an
// already-resolved immutable Decision into bounded credentials and verifies
// them against trusted receiver context.
package cellncapability
