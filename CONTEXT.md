# Token Usage Tracker

This context records the privacy-preserving identity terms used when tracking token usage by downstream API key.

## Language

**Downstream API Key**:
The client credential carried by a request to CLIProxyAPI. It is never returned by the tracker or persisted in the usage database.
_Avoid_: upstream key, provider key

**API Key Alias**:
A human-readable label for one downstream API key identity. The tracker associates it with a one-way fingerprint rather than the raw credential.
_Avoid_: API key name, key label

**Alias Configuration**:
The complete declarative set of API Key Aliases supplied through the plugin's own configuration fields. It is the sole write source for aliases.
_Avoid_: dashboard configuration, dynamic alias management

**Alias Selection**:
The set of API Key Aliases selected in the dashboard to narrow a usage view. Multiple selections match the union of their associated key identities.
_Avoid_: key search, alias grouping
