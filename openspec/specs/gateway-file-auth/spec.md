# gateway-file-auth Specification

## Purpose

Define file-based inbound authorization, service isolation and deployment rules for CKLogs and project-scoped Jira callers.

## Requirements

### Requirement: Single file is the source of inbound authorization
The gateway SHALL load inbound authorization from ./config.json relative to the process working directory, with GATEWAY_AUTH_FILE as an optional path override. The root SHALL contain a nonempty tokens array and MAY contain jira.projects. Each token entry SHALL contain token and exactly one of cklogs or jiraProjects. Missing/unreadable files, invalid JSON, duplicate or unknown keys, placeholder/empty/whitespace-containing tokens, duplicate tokens, and empty authorization SHALL fail startup without exposing secret values.

#### Scenario: Default file and multiple tokens
- **WHEN** the working directory contains a valid config.json with two cklogs entries and two Jira entries
- **THEN** the gateway authenticates all four tokens using that file without requiring an authorization environment variable

#### Scenario: Ambiguous or invalid entry
- **WHEN** a token is duplicated or declares both cklogs and jiraProjects
- **THEN** startup fails rather than merging permissions or preferring internal

### Requirement: Legacy authorization sources cannot expand file permissions
The gateway MUST reject nonempty GATEWAY_TOKEN_EXTERNAL, GATEWAY_TOKEN_INTERNAL, GATEWAY_AUTH_TOKENS, GATEWAY_JIRA_TOKENS or GATEWAY_JIRA_TOKENS_FILE when running the new file-based implementation. The gateway SHALL retain caller-side Authorization: Bearer semantics and the existing global CIDR restriction.

#### Scenario: Old internal token variable remains
- **WHEN** a valid auth file is provided but GATEWAY_TOKEN_INTERNAL is also nonempty
- **THEN** startup reports a sanitized configuration conflict without importing the environment token

### Requirement: cklogs retains two permission classes
cklogs SHALL accept only external or internal. External SHALL allow POST /v1/cklogs/delivery, /login and /message. Internal SHALL additionally allow POST /v1/cklogs/analysis/delivery, /auth and /ops. These routes SHALL retain their existing request/response validation and shared FIFO behavior. Internal SHALL NOT grant Jira access.

#### Scenario: External attempts analysis
- **WHEN** an external token calls /v1/cklogs/analysis/delivery
- **THEN** the response is 403 token_scope_forbidden and no Kibana query executes

#### Scenario: Internal uses self-service
- **WHEN** an internal token calls a valid self-service route
- **THEN** authorization succeeds with existing cklogs behavior

### Requirement: Jira permissions are project scoped and service isolated
jiraProjects SHALL be a nonempty array of distinct project keys fully matching [A-Z][A-Z0-9_]{0,63}, each referencing jira.projects. Wildcards SHALL be rejected. The gateway SHALL validate the token service domain and project before contacting Jira. Unknown tokens SHALL receive 401 unauthorized; cross-service use SHALL receive 403 token_scope_forbidden; missing project grants SHALL receive 403 project_access_forbidden. Tokens sharing a project SHALL share its policy, without per-token issue ownership or operation scopes.

#### Scenario: One token has multiple projects
- **WHEN** token A allows CS and IT, and token B only allows CS
- **THEN** A can use either project's fixed API and B receives 403 for IT without an upstream request

#### Scenario: Jira token attempts cklogs access
- **WHEN** a known Jira token calls /v1/cklogs/delivery
- **THEN** the response is 403 token_scope_forbidden even though the token is valid

### Requirement: Only configured services require their upstream settings
The gateway SHALL enable cklogs or Jira according to the presence of their token entries and SHALL validate enabled services' required configuration. Public health/readiness responses MUST NOT enumerate tokens or projects. Missing settings for a disabled service SHALL NOT prevent the other service from running.

#### Scenario: cklogs-only deployment
- **WHEN** the file contains only valid cklogs tokens and CK credentials are configured
- **THEN** Jira credentials and project metadata are not required for startup/readiness

### Requirement: External file mounting is the deployment contract
Container deployment SHALL bind-mount the host config.json read-only at /config.json with explicit working directory /, fail on a missing host source, and ensure UID 65532 can read the file. Real configuration SHALL be excluded from Git and Docker build context. Updates SHALL take effect after process restart or container recreation, without hot reload or image rebuild.

#### Scenario: Host editor replaces the file
- **WHEN** an operator replaces the host config.json and recreates the container with its existing image
- **THEN** the new token/project configuration is read from the remounted file

### Requirement: Existing cklogs callers can migrate without changing token values
Migration documentation SHALL map old external/internal token values into distinct file entries and legacy GATEWAY_AUTH_TOKENS values to external. Duplicate cross-class credentials SHALL require correction. Caller PLATFORM_GATEWAY_TOKEN and PLATFORM_GATEWAY_INTERNAL_TOKEN names and Bearer values SHALL remain usable when values are preserved.

#### Scenario: Existing external caller after deployment migration
- **WHEN** its former external token is moved unchanged into a file entry and old gateway variables are removed
- **THEN** the caller continues using its existing Bearer token and API contract
