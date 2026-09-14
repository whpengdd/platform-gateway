## ADDED Requirements

### Requirement: Resource endpoints are scoped by Jira project
The gateway SHALL expose GET /v1/jira/projects and, under /v1/jira/projects/{projectId}, GET /capabilities, POST /issues/search, GET /issues/{issueKey}, POST /issues, GET/POST /issues/{issueKey}/comments, GET /issues/{issueKey}/comments/{commentId}, POST /issues/{issueKey}/attachments and GET /issues/{issueKey}/attachments/{attachmentId}/content. Successful reads/search SHALL return 200 and successful creations SHALL return 201. It SHALL NOT expose /tickets, /binding, /changes or /operations as compatibility aliases.

#### Scenario: Caller supplies a Zammad binding request
- **WHEN** a request targets a superseded /tickets or /binding path
- **THEN** no Jira operation executes and the route is unavailable

### Requirement: Inputs and output fields have explicit boundaries
JSON inputs SHALL reject unknown/duplicate keys and trailing documents. Search SHALL accept only paired updatedFrom/updatedTo, issueKeys, text, limit, or a continuation cursor. Create SHALL accept only a fields object limited to configured createFields and fixed default keys. Comment creation SHALL accept only body. readFields SHALL be an exact field list with no wildcard/expand; defaults SHALL be summary/status/updated/reporter/creator/assignee. Personnel and attachment structures SHALL be projected to documented minimal values, not raw Jira objects.

#### Scenario: Caller requests unrestricted fields
- **WHEN** a request includes JQL, fields/expand search parameters or unsupported create fields
- **THEN** it is rejected before upstream execution rather than broadening the output or write scope

#### Scenario: Jira returns extra embedded data
- **WHEN** an upstream issue contains self URLs, avatars or fields not on the read list
- **THEN** these values are absent from the gateway response

### Requirement: Query pagination is bounded and not a sync cursor
Search SHALL default limit to 50 and cap it at 100, cap issueKeys at 100 and text at 256 bytes, and allow optional paired time bounds spanning at most 30 days. Continuation requests SHALL contain only cursor. Cursors SHALL authenticate the project, query, position, configuration and a 15-minute expiry; process restart can invalidate them. Responses SHALL use items/nextCursor and SHALL NOT expose unfiltered totals. The gateway SHALL document non-snapshot pagination and SHALL NOT retain a caller's synchronization watermark.

#### Scenario: Cursor used under another project
- **WHEN** an otherwise valid cursor is submitted to a different project path
- **THEN** it is rejected without issuing the original project's query

#### Scenario: Cursor expires or configuration changes
- **WHEN** the continuation is no longer valid for current configuration or time
- **THEN** the gateway returns 410 cursor_expired and does not silently restart a broader query

### Requirement: Comment visibility is checked separately from synchronization
Comment APIs SHALL recheck the parent issue and exclude group/role-restricted comments in the first implementation. They SHALL retain eligible historical/bot comments and SHALL NOT parse publication markers, inject backlinks or deduplicate article IDs. A comment POST SHALL write the supplied body once and return its ID.

#### Scenario: Restricted comment is requested by ID
- **WHEN** the comment has restricted group/role visibility even though the parent issue matches
- **THEN** the gateway returns 404 resource_not_available

### Requirement: Attachment transfer cannot become a URL proxy
Uploads SHALL accept exactly one file multipart part and bounded filename/type/content. Downloads SHALL resolve the attachment through its authorized parent issue and a validated same-origin secure/attachment URL. Arbitrary contentUrl, redirects, path traversal and cross-origin URLs SHALL be rejected. Download responses SHALL use attachment disposition and nosniff, and SHALL have a byte/deadline limit.

#### Scenario: Upstream attachment redirects outside Jira
- **WHEN** the content endpoint responds with a redirect
- **THEN** the gateway does not follow it or forward Jira credentials to the target

### Requirement: Writes report uncertain outcomes without automatic retry
The gateway SHALL perform writes synchronously without an operation ledger, automatic retries or an Idempotency-Key guarantee. A write possibly dispatched but lacking a confirmed result SHALL report 502 outcome_unknown when a response can be sent. A known upstream rejection SHALL return a sanitized rejection. Repeated caller submissions SHALL NOT be described as deduplicated. Read-only search POST SHALL follow read failure semantics.

#### Scenario: Jira commits and the response connection closes
- **WHEN** the gateway loses the upstream write response after possible dispatch
- **THEN** it reports outcome_unknown and does not issue a second write

### Requirement: Jira resource use is isolated and bounded
The gateway SHALL use an independent Jira queue with 2 execution slots and 32 waiting slots, bounded waits/operation deadlines, serial internal requests per operation, and per-project in-memory request/byte budgets. JSON request size SHALL be capped at 256 KiB, descriptions/comments at 32 KiB, individual attachments at 10 MiB, multipart at 11 MiB and upstream JSON at 4 MiB. Documentation SHALL identify limits as per-process and nonpersistent across restarts. Jira activity MUST NOT consume cklogs queue slots.

#### Scenario: Jira is saturated
- **WHEN** Jira execution and waiting capacity are exhausted
- **THEN** further Jira work receives a controlled busy response and eligible cklogs requests can still run

### Requirement: Credentials and diagnostics remain inside the gateway
Jira transport SHALL use the configured HTTPS origin/context path with certificate verification, no redirects and no implicit environment proxy. Client headers SHALL NOT replace upstream credentials or routing. Audit/error output SHALL omit raw tokens, JQL values, issue/comment bodies, attachment contents and upstream error bodies. Service/project/resource error codes SHALL be consistent with the configuration and filtering specs.

#### Scenario: Upstream error includes credentials or issue content
- **WHEN** Jira returns a verbose error body
- **THEN** only a sanitized error code/message and request correlation identifier reach the client or ordinary audit log

### Requirement: Caller documentation and deployment examples match the contract
The repository SHALL deliver a Jira API document with request/response examples, authentication, pagination, filtering, createDefaults behavior and outcome_unknown handling, plus a shared config example and cklogs migration guide. Documentation SHALL assign synchronization, binding and retry recovery to callers and SHALL distinguish mock validation from live Jira acceptance.

#### Scenario: Consumer implements from repository documentation
- **WHEN** it follows the published resource paths and configuration example
- **THEN** no undocumented Zammad binding service, background operation API or local filters/equals configuration is required

### Requirement: Field selection cannot expose nested resources
The supported read field set SHALL be summary/description/updated scalars, status projected to id/name, personnel projected to identity/displayName/email, attachment metadata projected to id/filename/size/mimeType, and explicitly configured customfield_<digits>. Custom fields SHALL support only metadata-confirmed scalars, options projected to id/value, minimal personnel, or arrays of those types. Unknown complex types SHALL be rejected; temporary metadata unavailability SHALL return 503 for affected reads. Read-type validation SHALL be independent of createEnabled and SHALL NOT require an issueTypeId. comment, issuelinks, subtasks, parent and worklog SHALL be rejected as readFields even if configured. No raw nested objects SHALL pass through.

#### Scenario: Linked issue is outside authorization
- **WHEN** configuration requests issuelinks or a resource-containing unsupported custom field
- **THEN** configuration/type validation rejects it instead of returning linked issue details

#### Scenario: Embedded restricted comment
- **WHEN** Jira includes comment data inside an issue response
- **THEN** it is omitted and callers must use the visibility-checked comment API

#### Scenario: Customer field remains available without issue creation
- **WHEN** an authorized issue has an explicitly allowed scalar customer field and creation is disabled
- **THEN** the gateway can return that field after its read type is confirmed

### Requirement: Comment cursors identify their parent and operation
All cursors SHALL authenticate the operation type in addition to project/query/position/configuration/expiry. Comment cursors SHALL also bind the stable parent issue ID. Every comment page SHALL recheck parent authorization and comment visibility. Cross-project, cross-parent or cross-operation reuse SHALL return 400 invalid_cursor without executing the cursor's original query; parent resolution for verification MAY occur. Expired/configuration-invalid cursors SHALL return 410 cursor_expired.

#### Scenario: Comment cursor is used for another issue or search
- **WHEN** a cursor for comments on CS-1 is supplied under CS-2 or issues/search
- **THEN** it is rejected as invalid_cursor without returning CS-1 comments
