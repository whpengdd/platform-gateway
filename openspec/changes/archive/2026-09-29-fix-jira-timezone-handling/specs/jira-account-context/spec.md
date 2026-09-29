## ADDED Requirements

### Requirement: Account context is shared within one authenticated Jira client
The gateway SHALL obtain account context through GET /rest/api/2/myself using the same configured Jira client and credentials as the dependent operation. It SHALL share one process-local cache across projects and inbound tokens using that client, and SHALL isolate different upstream/client credential configurations. Cached data SHALL be limited to reporter identity fields accountId/name/key, displayName/emailAddress, timezone identity and resolution state, expiry, and context generation. Consumers SHALL receive immutable snapshots or copies. Restart or client replacement SHALL discard the cache. Public capability/person projections SHALL retain their existing minimal shape.

#### Scenario: Projects share an account
- **WHEN** two authorized projects use the same Jira client and its account snapshot is fresh with the identity/timezone parts required by each consumer available
- **THEN** creation, capabilities and time-bounded searches reuse that snapshot without separate project-specific /myself reads

#### Scenario: Another client has different credentials
- **WHEN** another Jira client is constructed for a different upstream or credential configuration
- **THEN** it cannot reuse the first client's account data or context generation

#### Scenario: Upstream returns additional profile data
- **WHEN** /myself includes groups, roles, self URLs or avatars
- **THEN** the cache and public responses retain only the explicitly required account context fields

### Requirement: Account context cache lifetime is explicitly configurable
The configuration SHALL accept optional jira.accountCacheTtlSeconds as an integer from 0 through 3600, defaulting to 300 when omitted. Zero SHALL disable reuse of completed snapshots while preserving in-flight coalescing and the context history needed to compare cursors. Positive TTL SHALL start when a successful identity snapshot is obtained. Configuration changes SHALL take effect through the existing restart/recreation contract. Null, fractional, string, negative and out-of-range values SHALL fail configuration validation with a sanitized field-path diagnostic. Disabled Jira SHALL NOT require an account request to start.

#### Scenario: Existing configuration omits the setting
- **WHEN** jira.accountCacheTtlSeconds is absent from an otherwise valid configuration
- **THEN** the account cache lifetime is 300 seconds

#### Scenario: Operator disables retained caching
- **WHEN** jira.accountCacheTtlSeconds is explicitly 0, a previous refresh has completed, and no failure backoff applies to the requested context
- **THEN** a new account-dependent request triggers another refresh while retaining generation history

#### Scenario: Invalid cache configuration
- **WHEN** jira.accountCacheTtlSeconds is null, a string, fractional, negative or greater than 3600
- **THEN** startup rejects it and identifies jira.accountCacheTtlSeconds without echoing input values or credentials

### Requirement: Account refresh is demand driven and coalesced
The gateway SHALL refresh a missing or expired account snapshot only when an operation needs it and SHALL perform no background polling. At most one /myself request per client SHALL be in flight; concurrent consumers SHALL share that refresh result, including when TTL is zero. Refreshes and waits SHALL remain within the existing Jira queue, operation deadlines and upstream response bounds. A waiting caller's cancellation SHALL release that waiter without cancelling other callers. If the refresh owner is cancelled, its upstream request SHALL end and remaining live consumers SHALL be able to elect a new owner within their own deadline. Caller cancellation SHALL NOT be cached as an upstream failure.

#### Scenario: Concurrent cache miss
- **WHEN** several live requests require the same missing account context concurrently
- **THEN** only one /myself request runs and the callers consume its completed snapshot

#### Scenario: Idle process
- **WHEN** the cache expires with no account-dependent request
- **THEN** no /myself request is sent until a consumer arrives

#### Scenario: A waiting caller cancels
- **WHEN** one caller cancels while another live caller owns the account refresh
- **THEN** the cancelled waiter exits promptly and the refresh can still complete for the live caller

#### Scenario: Refresh owner cancels
- **WHEN** the caller owning an in-flight refresh is cancelled while another consumer remains live
- **THEN** the cancelled HTTP request terminates, no upstream-failure backoff is recorded, and a live consumer can perform one replacement refresh within its own deadline

### Requirement: Expired account context is not served after a failed refresh
An upstream transport, HTTP or invalid-identity failure SHALL NOT extend the previous snapshot's lifetime or make expired data usable. Failed account retrieval SHALL use a five-second per-client retry backoff, with the next dependent request after the backoff triggering recovery. Time-bounded searches lacking a usable identity snapshot SHALL return 503 upstream_unavailable. Creation and capabilities SHALL retain their existing unavailable-metadata behavior when identity cannot be obtained. Operations that do not need account context SHALL NOT be disabled solely by this cache failure. No write SHALL be automatically retried because of refresh failure or cache invalidation.

#### Scenario: Jira fails after snapshot expiry
- **WHEN** the account snapshot has expired and its refresh receives an upstream error
- **THEN** time-bounded search returns 503 upstream_unavailable without sending its business search or using the expired timezone

#### Scenario: Recovery after backoff
- **WHEN** another dependent request arrives after the five-second backoff and Jira returns valid account data
- **THEN** one refresh recovers the cache without process restart

#### Scenario: Read does not depend on account context
- **WHEN** a valid ordinary issue read or a search without caller time bounds can complete its existing upstream checks
- **THEN** account-cache failure alone does not block that operation

### Requirement: Identity availability is independent of timezone availability
A valid account identity with missing, empty, invalid or unsupported timeZone SHALL remain usable by creation and capabilities. Time-bounded searches SHALL require an explicitly resolvable IANA timezone or UTC. When a successfully retrieved profile lacks usable timezone metadata, new time-bounded searches SHALL return 503 timezone_unavailable. If a due independent timezone retry instead fails to retrieve a valid profile, time consumers SHALL return 503 upstream_unavailable for that attempt and its five-second failure backoff; identity consumers SHALL still be allowed to use identity data whose original TTL has not expired. A failed retry SHALL NOT renew that TTL or change generation. The gateway SHALL reject the host-dependent Local timezone and SHALL NOT fall back to host/container timezone, UTC, a current numeric offset or an older snapshot's timezone. A new successful profile response SHALL replace the previous timezone state even if the new state is unavailable. Time consumers SHALL retry unavailable timezone metadata on demand after a five-second backoff independently of a longer valid identity TTL.

#### Scenario: Valid bot has no timezone
- **WHEN** /myself provides a valid bot identity but omits timeZone
- **THEN** creation and capabilities can use that identity while a new time-bounded search returns 503 timezone_unavailable

#### Scenario: Previously known timezone disappears
- **WHEN** a refresh replaces a valid timezone with a missing or unsupported timeZone
- **THEN** subsequent time searches do not reuse the former timezone and the context generation changes

#### Scenario: Missing timezone is corrected
- **WHEN** the cached identity is fresh but its timezone is unavailable and the five-second timezone retry delay has elapsed
- **THEN** the next time consumer can refresh /myself and use a newly valid timezone without waiting for the entire identity TTL

#### Scenario: Independent timezone retry fails while identity is fresh
- **WHEN** identity data is still within its original TTL, its timezone is unavailable, and an independent timezone refresh receives an upstream error
- **THEN** time consumers receive 503 upstream_unavailable for that attempt and its five-second backoff, identity consumers can continue using the unexpired identity, and neither identity expiry nor context generation changes

#### Scenario: Short or disabled snapshot TTL does not disable timezone backoff
- **WHEN** accountCacheTtlSeconds is zero or shorter than five seconds and a successful profile response lacks a usable timezone
- **THEN** time consumers return timezone_unavailable without another profile request until the five-second retry boundary, retain the observed generation for cursor checks, and do not return expired identity data; identity-only consumers still follow their own snapshot TTL

### Requirement: Context generation tracks semantic changes rather than refreshes
The client SHALL establish a context generation on its first successful identity snapshot and SHALL retain generation history independently of snapshot expiry. Subsequent changes to the stable account identity or timezone state, including available-to-unavailable and unavailable-to-available transitions, SHALL advance the generation. Identical refreshes, display-only identity changes and normal date-dependent DST offsets SHALL NOT advance it. All search cursors SHALL capture the generation known to their operation; first baseline establishment SHALL NOT by itself invalidate searches issued before any account context was loaded. A search SHALL use the same immutable generation snapshot for query construction and next-cursor issuance.

#### Scenario: Identical profile is refreshed
- **WHEN** a profile is refreshed with the same stable identity and timezone
- **THEN** its TTL is renewed and existing search cursors retain the same generation

#### Scenario: Account timezone changes
- **WHEN** a subsequent successful refresh detects UTC changing to Asia/Shanghai for the same account
- **THEN** the generation advances and old search generations are distinguishable from the new context

#### Scenario: Concurrent refresh during a search
- **WHEN** the account generation changes after a search has captured its query context
- **THEN** any next cursor from that search carries the original captured generation rather than the newer cache generation

### Requirement: Account caching does not replace authorization or field validation
Every request SHALL retain the existing inbound token/project checks before account access, parent issue/filter checks, child visibility checks and write-result verification. Reporter identity selection SHALL preserve the existing accountId/name/key behavior. createEnabled SHALL be recomputed from current configuration, account snapshot and existing creation metadata checks. This capability SHALL NOT cache /field schemas, creation metadata, authorization results or write outcomes. Cache errors and diagnostics SHALL omit raw credentials, profile response bodies and JQL.

#### Scenario: Parent access changes while the profile is cached
- **WHEN** the account snapshot is fresh but an issue no longer satisfies project filtering
- **THEN** issue, comment and attachment operations still perform the existing parent checks and deny access

#### Scenario: Unknown caller causes a cache miss
- **WHEN** an unauthorized caller requests an account-dependent endpoint while the cache is empty
- **THEN** normal authentication rejects the request before /myself is contacted

### Requirement: Cache freshness limits are documented
Documentation SHALL identify TTLs and failures as per-client, per-process and nonpersistent. It SHALL explain that a Jira timezone change can remain undetected until refresh, that TTL zero still cannot make /myself and search atomic, and that process restart/recreation clears the cache and cursors. It SHALL distinguish these search changes from creation-time storage or UI display changes and SHALL describe removing the new configuration key and restoring the old UTC-account prerequisite before rollback to a pre-change binary.

#### Scenario: Operator changes account timezone
- **WHEN** an operator follows the deployment guidance to change Jira account timezone
- **THEN** the guidance calls for restarting/recreating the gateway or accepting the configured detection delay, without promising immediate or atomic detection
