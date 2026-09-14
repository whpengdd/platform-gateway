## MODIFIED Requirements

### Requirement: Single file is the source of inbound authorization
The gateway SHALL load inbound authorization from ./config.json relative to the process working directory, with GATEWAY_CONFIG_FILE as an optional path override, following the gateway-application-config single-source contract. The root SHALL contain a nonempty tokens array and SHALL accept jira, cklogs, server and audit as defined by that contract. Each token entry SHALL contain token and exactly one of cklogs or jiraProjects. Missing/unreadable files, invalid JSON, duplicate or unknown keys, placeholder/empty/whitespace-containing tokens, duplicate tokens, and empty authorization SHALL fail startup without exposing secret values.

#### Scenario: Default file and multiple tokens
- **WHEN** the working directory contains a valid config.json with two cklogs entries and two Jira entries
- **THEN** the gateway authenticates all four tokens using that file without requiring an authorization environment variable

#### Scenario: Ambiguous or invalid entry
- **WHEN** a token is duplicated or declares both cklogs and jiraProjects
- **THEN** startup fails rather than merging permissions or preferring internal

### Requirement: Legacy authorization sources cannot expand file permissions
The gateway SHALL NOT read or inspect obsolete inbound authorization environment variables for configuration, warnings or startup rejection, as defined by gateway-application-config. Residual values SHALL NOT add permissions, override file settings or prevent startup; only tokens in the selected file SHALL grant access. The gateway SHALL retain caller-side Authorization: Bearer semantics and the existing global CIDR restriction.

#### Scenario: Old internal token variable remains
- **WHEN** a valid application file is provided and GATEWAY_TOKEN_INTERNAL is also nonempty
- **THEN** startup succeeds without inspecting or importing the environment token, and that token grants no access unless separately present in the file
