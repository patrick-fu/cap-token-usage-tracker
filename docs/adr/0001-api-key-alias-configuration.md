# ADR 0001: Use plugin configuration as the API key alias write source

- Status: Accepted
- Date: 2026-08-07

## Context

API Key Aliases label downstream API key identities in usage views. The label is
metadata, while the raw credential is sensitive input. A mutable management API
would add another secret-bearing write path and make the effective alias set
harder to review, back up, and reproduce.

## Decision

The plugin's `api_key_aliases` configuration is the complete declarative
source-of-truth for aliases. Configuration reconciliation is the only write
entry point: management endpoints may read the effective alias list, but do not
provide `PUT` or `DELETE` alias mutations. The dashboard only offers alias
selection; selecting several aliases matches the union of their key identities.

The field is a list of explicit `{api_key, alias}` entries:

```yaml
api_key_aliases:
  - api_key: "sk-xxxxxxxx"
    alias: "Production-OpenAI"
```

The plugin computes a one-way fingerprint from each configured downstream key
and keeps the effective fingerprint, suffix, and alias metadata in memory for
historical queries. It never persists or returns the raw key (and alias records
are not a second bbolt write source). Configuration changes are therefore
auditable as configuration changes and apply to historical views at query time.

## Consequences

Positive:

- One reviewable, reproducible write path for alias ownership and changes.
- No management UI or API needs to accept raw credentials for mutation.
- Alias selection remains useful for historical data without rewriting records.

Trade-offs:

- Renaming, adding, or removing an alias requires changing and reloading plugin
  configuration rather than issuing an interactive API request.
- The configuration necessarily contains the credentials needed to derive
  identities; protect its file, access controls, backups, and deployment logs.
- Existing records without a key identity cannot match an alias filter.

## Rejected alternatives

- **Mutable `PUT`/`DELETE` alias endpoints:** convenient for interactive edits,
  but create a second source-of-truth and an additional raw-key handling path.
- **Dashboard-managed aliases:** unsuitable because the dashboard is a view and
  should not become a credential-management surface.
