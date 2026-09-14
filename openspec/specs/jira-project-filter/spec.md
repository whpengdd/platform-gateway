# jira-project-filter Specification

## Purpose

Constrain Jira resource access and creation to authorized projects and their configured filtering and field policies.

## Requirements

### Requirement: Projects define optional JQL filtering
Each configured Jira project SHALL accept optional filterJql, issueTypeId, createDefaults, createFields and readFields. filterJql SHALL be a condition expression with balanced syntactic boundaries and no ORDER BY clause. The gateway SHALL reject the superseded filters/equals format rather than ignoring it. Absent/blank filterJql SHALL impose no additional issue restriction within the authorized project and Jira account visibility.

#### Scenario: Project has no filter
- **WHEN** token A is granted CS and CS has no filterJql
- **THEN** any CS issue visible to the gateway's Jira account is eligible for the fixed APIs without a separate binding

#### Scenario: Configured JQL is invalid or cannot be validated
- **WHEN** Jira rejects the condition or cannot be reached for semantic validation
- **THEN** the project is disabled or temporarily unavailable and no request falls back to unfiltered access

### Requirement: Project restriction is mandatory in every query
The gateway SHALL combine a fixed project clause, parenthesized configured filterJql, and encoded client query values using AND. It SHALL prevent configured fragments or request values from escaping these boundaries, control sorting separately, and validate actual project membership in results. Clients MUST NOT submit or override filterJql or arbitrary JQL.

#### Scenario: Filter contains OR
- **WHEN** CS configures labels = zammad OR labels = support
- **THEN** the entire OR expression remains conjunctive with project CS and cannot return another project's resources

#### Scenario: Client text contains JQL-looking syntax
- **WHEN** text or an identifier contains quotes, operators or parentheses
- **THEN** it is rejected or encoded as a value and cannot alter the project's authorization query

### Requirement: Parent issue filtering protects all child resources
For issue reads and existing-issue writes, the gateway SHALL establish current project membership and filter match through a scoped Jira query. Comments and attachments SHALL additionally be verified as belonging to that parent issue. A failed match or nonexistent resource SHALL receive 404 resource_not_available; upstream validation failure SHALL return unavailability rather than permit access. Neither valid child IDs nor authorization caches SHALL bypass the parent check.

#### Scenario: Attachment belongs to another issue
- **WHEN** a caller uses an attachment ID from an inaccessible issue under an accessible issue path
- **THEN** no file is downloaded and the gateway returns 404

#### Scenario: Parent no longer matches
- **WHEN** the scoped Jira query reports that a formerly accessible issue no longer satisfies filterJql
- **THEN** subsequent issue/comment/attachment access is denied, including writes

### Requirement: JQL evaluation does not become synchronization policy
The gateway SHALL delegate JQL semantics to Jira rather than locally executing arbitrary JQL. It SHALL NOT require ticket binding, import an issue allowlist, maintain sync progress, or select webhook/polling schedules. Documentation SHALL state that index latency and check/write races preclude immediate revocation or atomic authorization guarantees.

#### Scenario: Historical or bot comment on an eligible issue
- **WHEN** the comment passes resource and visibility checks
- **THEN** the gateway does not exclude it based on binding age, bot authorship or synchronization markers

### Requirement: Creation enforces non-overridable defaults
POST issue SHALL fix project, configured issueTypeId and bot reporter. createDefaults SHALL supply mandatory field values; equal client-supplied values are acceptable but conflicting values SHALL return 400 create_default_conflict before POSTing to Jira. Defaults and caller fields SHALL pass supported Jira creation metadata validation. Project/type/reporter/assignee/security overrides SHALL be rejected in both sources.

#### Scenario: Conflicting label
- **WHEN** createDefaults fixes labels to ["zammad"] but the request provides ["other"]
- **THEN** the gateway returns 400 without creating an issue

### Requirement: Filtered creation requires a supported proof
The gateway SHALL enable creation only when required create configuration/metadata is available and the full filter can be verified before writing. The first implementation SHALL support only one positive labels equality with a fixed string covered by createDefaults, optionally enclosed in outer parentheses. The finite creation checker MUST consume the complete expression. AND, OR, cf[n], functions, historical predicates, unsupported field types, uncovered conditions or unavailable metadata SHALL disable creation without disabling otherwise valid existing-issue APIs.

#### Scenario: Simple label equality is covered
- **WHEN** filterJql is labels = zammad, createDefaults contains that label, and creation metadata accepts all required fields
- **THEN** capabilities reports createEnabled and the gateway can create within the fixed project using those defaults

#### Scenario: Complex filter is valid for reading but not creation
- **WHEN** filterJql uses a function or OR that the finite creation checker cannot prove
- **THEN** search and existing-issue operations use Jira evaluation, while creation returns 403 operation_not_available

#### Scenario: Jira changes data during creation
- **WHEN** the returned issue's project or required fixed fields cannot be confirmed after creation
- **THEN** the gateway returns outcome_unknown without automatically correcting or recreating the issue

### Requirement: Transient validation failures recover on demand
After transient Jira validation failure the gateway SHALL return 503 and retry validation on the first request after a fixed 5-second backoff. Only one validation per project SHALL be in flight; concurrent callers SHALL receive 503. Successful validation SHALL restore service without restart. A definitively invalid condition SHALL remain disabled until configuration is corrected and the process restarted. No background scheduler SHALL be required.

#### Scenario: Jira recovers after startup outage
- **WHEN** initial validation fails transiently and Jira becomes available after the backoff
- **THEN** the next request triggers validation and successful validation restores filtered access

#### Scenario: Concurrent recovery requests
- **WHEN** multiple requests arrive for an unvalidated project after the backoff
- **THEN** at most one validation runs and no caller bypasses filtering

#### Scenario: Custom fields used for creation but not proof
- **WHEN** the filter is labels = zammad and supported business custom fields are required by creation metadata
- **THEN** those fields may be supplied through createFields or createDefaults without enabling cf[n] creation proof
