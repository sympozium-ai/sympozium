#!/usr/bin/env python3
"""Builds the `celln starter-configure` plan of one backend.

Usage: fleet-plan.py PACKAGE PACKAGE_HASH PRINCIPAL BACKEND OUTPUT
Reads FLEET_BACKENDS, FLEET_SCOPE, FLEET_LIMIT_* and FLEET_HTTPS_HOSTS from
the environment and prints the plan as one JSON line.

A backend's model parameters join the plan only when it has some: a plan
without them is byte-identical to the one built before parameters existed,
and a Celln up to v0.5.22 refuses a plan that names them.

Likewise a backend's maxOutputTokens (output tokens per model request, 256 to
4096) joins the plan only when it is set and not Celln's default of 512: a
Celln up to v0.5.23 refuses a plan that names it.
"""
import json
import os
import sys

DEFAULT_MAX_OUTPUT_TOKENS = 512


def max_output_tokens(backend):
    """The backend's non-default output cap per request, else None."""
    value = backend.get("maxOutputTokens")
    if value in (None, 0, DEFAULT_MAX_OUTPUT_TOKENS):
        return None
    if isinstance(value, bool) or not isinstance(value, int) or not 256 <= value <= 4096:
        raise SystemExit(f"backend {backend['name']}: maxOutputTokens must be an integer from 256 to 4096")
    return value


def build(package, package_hash, principal, name, output, environ):
    backend = next(b for b in json.loads(environ["FLEET_BACKENDS"]) if b["name"] == name)
    scope = environ["FLEET_SCOPE"]
    plan = {"apiVersion": "celln.native-starter-config/v1", "package": package, "packageHash": package_hash,
            "principal": principal, "credentialFile": backend["credentialFile"], "output": output}
    if backend.get("endpoint"):
        # The operator's model route; Celln validates it again before configuring.
        plan["modelConnection"] = {
            "provider": backend["provider"],
            "protocol": backend["protocol"],
            "endpoint": backend["endpoint"],
            "model": backend["model"],
            "credentialProfile": scope if name == "native" else f"{scope}-{name}",
            "allowInsecure": bool(backend.get("allowInsecure", False)),
        }
        parameters = backend.get("parameters")
        if parameters:
            if not isinstance(parameters, dict):
                raise SystemExit(f"backend {name}: parameters must be a JSON object")
            # Merged by the Celln host into every provider request of this
            # backend; the guest never sees them. Celln validates them.
            plan["modelConnection"]["parameters"] = parameters
        tokens = max_output_tokens(backend)
        if tokens is not None:
            # A turn reserves 6 requests of this size; Celln checks hostLimits
            # against it and needs a starter package built for it.
            plan["modelConnection"]["maxOutputTokens"] = tokens
    elif backend.get("parameters"):
        raise SystemExit(f"backend {name}: parameters need the backend's own model route (endpoint)")
    elif max_output_tokens(backend) is not None:
        raise SystemExit(f"backend {name}: maxOutputTokens needs the backend's own model route (endpoint)")
    limits = {}
    for key, env in (("leaseSeconds", "FLEET_LIMIT_LEASE_SECONDS"), ("maxTurns", "FLEET_LIMIT_MAX_TURNS"),
                     ("maxModelRequests", "FLEET_LIMIT_MAX_MODEL_REQUESTS"), ("maxOutputTokens", "FLEET_LIMIT_MAX_OUTPUT_TOKENS")):
        value = int(environ.get(env, "0") or 0)
        if value > 0:
            limits[key] = value
    if limits:
        plan["hostLimits"] = limits
    hosts = json.loads(environ.get("FLEET_HTTPS_HOSTS") or "[]")
    if hosts:
        plan["httpsHosts"] = hosts
    return plan


if __name__ == "__main__":
    if len(sys.argv) == 3 and sys.argv[1] == "--has-parameters":
        backend = next(b for b in json.loads(os.environ["FLEET_BACKENDS"]) if b["name"] == sys.argv[2])
        sys.exit(0 if backend.get("parameters") else 1)
    if len(sys.argv) == 3 and sys.argv[1] == "--has-max-output-tokens":
        backend = next(b for b in json.loads(os.environ["FLEET_BACKENDS"]) if b["name"] == sys.argv[2])
        sys.exit(0 if max_output_tokens(backend) is not None else 1)
    print(json.dumps(build(*sys.argv[1:6], os.environ)))
