## Purpose

Define a single, strict file-based configuration contract for gateway runtime settings and upstream connections, so deployments have one authoritative application configuration while existing caller APIs remain stable.

## ADDED Requirements

### Requirement: Application configuration has one file source
The gateway SHALL load application configuration from GATEWAY_CONFIG_FILE when nonempty, otherwise ./config.json relative to the process working directory. The root SHALL accept only tokens, jira, cklogs, server and audit. The gateway SHALL preserve existing token and project authorization rules. Missing or unreadable files, files larger than 1 MiB, malformed JSON, duplicate keys, unknown keys, explicit null for typed settings and invalid values SHALL fail startup without logging credentials or raw configuration. Only absent optional settings SHALL receive defaults; environment interpolation inside JSON SHALL NOT occur. Relative audit paths SHALL resolve against the process working directory.

#### Scenario: Default configuration
- **WHEN** a valid ./config.json exists and GATEWAY_CONFIG_FILE is absent
- **THEN** all application settings are loaded from that file and documented defaults

#### Scenario: Explicit file path
- **WHEN** GATEWAY_CONFIG_FILE selects a valid alternate file
- **THEN** that file is used without merging ./config.json

#### Scenario: Invalid configuration is not repaired silently
- **WHEN** a typed setting is null, unknown, duplicated or invalid
- **THEN** startup fails with a sanitized diagnostic instead of using a default

### Requirement: Obsolete application environment does not participate in configuration
The gateway SHALL NOT read or inspect obsolete application environment variables for configuration, validation, warnings or startup rejection. This includes the retired GATEWAY_AUTH_FILE selector and all former inbound-token, Jira, CKLogs, listener, CIDR and audit settings. Their presence and values SHALL NOT change effective configuration, permissions, file selection or startup success. The gateway SHALL NOT maintain an obsolete-variable rejection list or support aliases, fallback or environment overrides for the migrated settings. GATEWAY_CONFIG_FILE SHALL remain the application configuration file selector. Deployment-only parameters and standard system environment such as proxy and certificate settings SHALL retain their separate roles and existing transport behavior. This retirement SHALL be scoped to obsolete variables duplicating the file settings and the retired file selector; it SHALL NOT prohibit external environment input or future independent environment-based features. Generic environment-file loading and pass-through SHALL be allowed, including stale variables, without key-specific interpretation, validation or application of obsolete values.

#### Scenario: Stale upstream environment conflicts with the file
- **WHEN** a valid file is present and a residual JIRA_BASE_URL names a different server
- **THEN** startup succeeds using jira.baseUrl without inspecting or warning about the obsolete variable

#### Scenario: Retired file selector remains
- **WHEN** GATEWAY_AUTH_FILE is nonempty and the file selected by GATEWAY_CONFIG_FILE or the default path is valid
- **THEN** the selected file is loaded and the obsolete selector has no effect on startup or file selection

#### Scenario: Old inbound token remains
- **WHEN** a valid file is present and residual GATEWAY_TOKEN_INTERNAL contains a token absent from that file
- **THEN** startup succeeds without inspecting or warning about the variable, and that token grants no access

### Requirement: Jira projects share a file-configured upstream
For enabled Jira, jira.baseUrl and jira.auth SHALL be required. All jira.projects SHALL use that one server and authentication object. baseUrl SHALL be an absolute HTTP or HTTPS server URL allowing a deployment context path, without embedded credentials, query, fragment or unsafe path encoding/traversal; trailing slashes SHALL be normalized. Enabled Jira over HTTP SHALL emit a startup warning without rejecting the configuration; the warning SHALL NOT include URL values or credentials. Operators SHALL supply the server URL without the REST API suffix, and the client SHALL append /rest/api/2/... as before. auth.type SHALL be bearer with a nonempty token or basic with nonempty username and password, exclusively; mixed or incomplete credentials and CR/LF in credentials SHALL fail startup. Inbound tokens SHALL remain separate from upstream credentials. The gateway SHALL preserve credential bytes without shell or environment expansion.

#### Scenario: Context path and shared server
- **WHEN** jira.baseUrl is https://jira.example.test/jira and CS and IT are authorized projects
- **THEN** both projects use that server and search requests target /jira/rest/api/2/search

#### Scenario: Invalid authentication
- **WHEN** bearer authentication also supplies username/password, or basic authentication omits password
- **THEN** startup fails before making an upstream request

#### Scenario: Credential punctuation
- **WHEN** an upstream password contains dollar signs or quotes in valid JSON
- **THEN** the exact decoded value is used for authentication

### Requirement: CKLogs settings and queue are configured in the file
The gateway SHALL read cklogs.baseUrl, auth.type/basic username/password, index, timeoutMs and queue.maxConcurrency/size/waitTimeoutMs from the file. Absent optional settings SHALL default to https://ck-logs.icoremail.net, mtatrans_distributed, 60000 ms, 2 concurrent requests, 32 waiting requests and 30000 ms queue wait respectively. Enabled CKLogs SHALL require nonempty Basic credentials. Explicit baseUrl SHALL be an absolute HTTP or HTTPS URL without embedded credentials, query or fragment; index SHALL be nonempty. Timeout and queue wait SHALL be positive integer milliseconds. cklogs.timeoutMs SHALL be at most floor((MaxInt64 - 5,000,000,000) / 1,000,000), reserving the existing HTTP client timeout margin of 5 seconds; queue.waitTimeoutMs SHALL be at most floor(MaxInt64 / 1,000,000). Bounds SHALL be checked before duration conversion and addition, and the assembled HTTP client timeout SHALL equal the configured duration plus 5 seconds without overflow; maxConcurrency SHALL be an integer >= 1 and size an integer >= 0 representable by the runtime integer. Existing shared FIFO and API semantics SHALL remain unchanged.

#### Scenario: No waiting queue
- **WHEN** cklogs.queue.size is explicitly 0
- **THEN** no waiting slots are created and 0 is not replaced by the default 32

#### Scenario: Invalid numeric settings
- **WHEN** a timeout is zero, negative, fractional or exceeds the supported duration range
- **THEN** startup fails instead of falling back or overflowing

#### Scenario: HTTP timeout addition boundary
- **WHEN** cklogs.timeoutMs equals floor((MaxInt64 - 5,000,000,000) / 1,000,000)
- **THEN** validation accepts the value and the assembled HTTP client timeout is positive and equals the configured duration plus 5 seconds
- **AND** a value one millisecond larger fails validation before conversion or addition

### Requirement: Server and audit settings have explicit defaults
The gateway SHALL read server.listenAddr with default :8091, server.allowCidrs as a CIDR string array with default [], audit.enabled with default true and audit.dir with default logs. An empty CIDR array SHALL impose no application CIDR restriction, preserving the current empty-allowlist behavior. Invalid CIDRs or listen addresses SHALL fail startup. audit.enabled=false SHALL disable the JSONL audit writer without disabling ordinary process logs. When enabled, audit.dir SHALL be nonempty and writable at initialization; off and - SHALL NOT act as disable aliases. Nonempty audit directory strings SHALL be interpreted as paths. Before the HTTP server starts listening, the audit writer SHALL verify under the actual process identity that the directory permits new file creation and that the current-day audit file can be opened for append/create/write. Failure SHALL abort startup, including when the directory already exists or the current-day file is unwritable. Initialization SHALL NOT truncate existing logs or emit synthetic audit records, and temporary probe files SHALL be removed. Disabled auditing SHALL skip these filesystem checks.

#### Scenario: Audit explicitly disabled
- **WHEN** audit.enabled is false and audit.dir is omitted
- **THEN** the gateway starts without creating an audit writer or audit directory

#### Scenario: Audit initialization cannot write
- **WHEN** auditing is enabled and the process cannot create files in the audit directory or cannot open the current-day log for append
- **THEN** startup fails before HTTP listening, even if the directory already exists

#### Scenario: Existing audit log is preserved
- **WHEN** auditing is enabled and initialization succeeds with an existing writable current-day log
- **THEN** its contents are preserved, no synthetic audit record is added and no temporary probe file remains

#### Scenario: Source restriction
- **WHEN** server.allowCidrs contains a valid restricted range
- **THEN** requests outside that range are rejected using the existing authorization behavior

### Requirement: Required upstream configuration depends on service authorization
The gateway SHALL enable services from their token entries. Required upstream settings for disabled services SHALL be optional, while explicitly supplied values SHALL still undergo structural and value validation. Existing token uniqueness, exclusive service scope, project references and project policy validation SHALL be retained. Public health/readiness responses SHALL NOT disclose credentials or project lists.

#### Scenario: Jira-only deployment
- **WHEN** the file has valid Jira tokens and complete Jira settings but omits cklogs
- **THEN** startup does not require CKLogs credentials

#### Scenario: CKLogs-only deployment
- **WHEN** the file has valid CKLogs tokens and credentials but omits jira
- **THEN** startup does not require Jira settings

### Requirement: Deployment supplies the new file contract directly
Host, self Compose and install/149 deployment paths SHALL use the new configuration without applying obsolete environment values to file settings. Deployment declarations SHALL remove dedicated mappings and defaults for obsolete application variables while retaining env_file and unrelated external environment input. Stale variables passed through env_file or inherited environment SHALL be allowed and SHALL have no effect on the migrated settings. Deployment tooling SHALL use --config-file instead of --auth-file and SHALL NOT support --listen as an application override. Compose SHALL retain deployment-only image, host port and mount parameters; the host config source variable SHALL be PLATFORM_GATEWAY_CONFIG_HOST_FILE. Deployment scripts SHALL NOT specifically read, interpret or detect the retired host config source variable or other obsolete application variables; residual inherited values SHALL NOT cause warnings, startup rejection or configuration overrides. Containers SHALL mount the configuration read-only at /config.json, set working directory /, reject a missing mount source and allow UID 65532 to read it. Real configuration SHALL remain excluded from Git and Docker build context. Updates SHALL take effect after host process restart or container recreation, without hot reload or image rebuild for configuration-only changes. Deployment instructions SHALL require matching image, configuration and scripts, and matching listener/log paths to port/mount settings.

#### Scenario: Container file replacement
- **WHEN** an operator atomically replaces the host file and recreates a container using the existing image
- **THEN** the new settings are loaded from the read-only remounted file

#### Scenario: File-only deployment paths
- **WHEN** each supported deployment path starts with a new-format file and no application environment values
- **THEN** its service can start and its effective configuration comes from that file

#### Scenario: External environment remains available
- **WHEN** env_file supplies proxy/certificate settings, unrelated external variables and stale application variables conflicting with a valid JSON file
- **THEN** the external variables remain available in the container and the migrated settings come only from JSON
- **AND** CKLogs retains environment proxy and NO_PROXY behavior while Jira continues to bypass proxies

### Requirement: Configuration relocation preserves business APIs
The gateway SHALL preserve /v1/jira/projects/... and CKLogs route paths, request/response contracts, Bearer authorization semantics and existing Jira REST API calls. Callers SHALL NOT need to supply a Jira server URL or change their requests due to this configuration relocation. This change SHALL NOT introduce per-project servers or multi-server routing.

#### Scenario: Existing caller
- **WHEN** deployment adopts the new configuration while preserving its inbound token and project policies
- **THEN** the caller continues using the same API requests and Bearer token

### Requirement: Actionable configuration diagnostics
Startup configuration failures SHALL identify the configuration file, schema field path or array index, and reason without including input values or arbitrary unknown keys. JSON structural errors SHALL include a nearby byte offset. Errors SHALL preserve ErrConfig identity for errors.Is checks.

#### Scenario: Missing Basic password
- **WHEN** jira.auth.type is basic and password is missing or empty
- **THEN** startup fails with jira.auth.password and a required-for-basic explanation, without logging credentials

#### Scenario: HTTP Jira upstream
- **WHEN** enabled Jira uses a valid HTTP baseUrl
- **THEN** configuration and transport accept it, startup warns that credentials and data lack TLS encryption, and requests use the configured HTTP origin
