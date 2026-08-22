# 0001. The wire protocol lives in the gateway repository

Status:   Accepted
Date:     2026-08-21
Decision: Publish the gateway-to-control-plane protocol here, under Apache 2.0, and have the proprietary control plane import it as a module dependency.

## Context

The AEGIS AI Gateway is Apache 2.0. The AEGIS Control Plane is proprietary and
lives in a separate repository. Something has to define the messages they
exchange, and it can live in either repository or in a third.

Apache 2.0 code may be used inside proprietary work. The reverse is not true.
Whichever repository owns the protocol determines which direction the
dependency can run.

## Decision

`api/controlplane/v1` lives here, Apache 2.0, as Go types plus JSON Schema
documents embedded through `embed.FS`. The control plane imports it.

## Consequences

- The dependency runs Apache into proprietary, which the licence permits.
  Nothing from the control plane can ever be required here.
- A customer can read exactly what their gateway would transmit before deciding
  to transmit it. For a product whose claim is that it retains no payload, the
  protocol being readable without a commercial relationship is part of the
  claim rather than a courtesy.
- A third party can implement a control plane against this protocol. That is
  accepted: the protocol is not the product.
- Once tagged, this package is a contract. Fields may be added; existing fields
  may not change name, type, or meaning. A breaking change needs a new
  versioned package.
- The control plane resolves the package through a `replace` directive to a
  sibling checkout until the first tagged release carries it.
