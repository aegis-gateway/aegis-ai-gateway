# 0003. No JSON Schema validator in the gateway

Status:   Accepted
Date:     2026-08-21
Decision: Publish the schemas here and keep the validator that reads them in the control plane. Guard the two against drift with a reflection test that needs no dependency.

## Context

The protocol ships JSON Schema documents as the normative description of the
wire format for implementations that are not written in Go. Validating an
instance document against them needs a JSON Schema library, and the gateway has
none. `xeipuuv/gojsonpointer` and `xeipuuv/gojsonreference` appear in the
module graph, but they are transitive dependencies of the policy engine and are
not a validator.

Adding one here would put a dependency into the Apache core purely to serve a
proprietary consumer.

## Decision

The schemas are published here and embedded through `embed.FS`. The validator
lives in the control plane, which compiles them from the embedded filesystem in
this module rather than from a vendored copy.

This package's own tests enforce the property a round trip cannot catch: that
the Go types and the schema documents describe the same message. Reflection
over the struct tags compares the declared properties and the required set
against the schema, so a field added to one and not the other fails here, in
the repository that owns the contract.

## Consequences

- The gateway gains no dependency. `api/controlplane/v1` is stdlib only.
- The control plane validates against the schemas in this module, so a schema
  edit here is felt there immediately. Validating against a vendored copy would
  mean validating against whatever was last remembered to sync.
- Instance validation is not available to a gateway-side emitter without adding
  a dependency. The emitter uses the Go type's own `Validate` method instead,
  which is the same rules expressed in Go.
- Writing the schemas by hand introduced a bug that the reflection test could
  not see: a control-character exclusion pattern written with doubled
  backslashes decodes to literal escape text and fails to compile as a regex.
  The control plane's validator caught it before the package shipped, which is
  the argument for the validator existing at all.
