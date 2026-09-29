## MODIFIED Requirements

### Requirement: Query pagination is bounded and not a sync cursor
Search SHALL default limit to 50 and cap it at 100, cap issueKeys at 100 and text at 256 bytes, and allow optional paired time bounds spanning at most 30 days. Continuation requests SHALL contain only cursor. Cursors SHALL authenticate the project, query, position, configuration and a 15-minute expiry; process restart can invalidate them. Search cursors SHALL additionally bind the captured Jira account-context generation. A continuation SHALL be rejected with 410 cursor_expired when a different generation is known before its business search. A time-bounded continuation SHALL obtain account context according to its TTL policy before comparing generations; one without caller time bounds SHALL compare the already known generation without forcing a profile refresh. The query and any next cursor SHALL use the same captured snapshot. Responses SHALL use items/nextCursor and SHALL NOT expose unfiltered totals. The gateway SHALL document non-snapshot pagination, timezone-change detection delay and SHALL NOT retain a caller's synchronization watermark. Comment cursor rules SHALL remain independent of account-timezone generations.

#### Scenario: Cursor used under another project
- **WHEN** an otherwise valid cursor is submitted to a different project path
- **THEN** it is rejected without issuing the original project's query

#### Scenario: Cursor expires or configuration changes
- **WHEN** the continuation is no longer valid for current configuration or time
- **THEN** the gateway returns 410 cursor_expired and does not silently restart a broader query

#### Scenario: Detected timezone change invalidates continuation
- **WHEN** a refresh detects that the account timezone changed after a search cursor was issued
- **THEN** a subsequent continuation using that older generation returns 410 cursor_expired without executing its business search

#### Scenario: Known-invalid cursor does not require another refresh
- **WHEN** another operation already detected a newer generation and the account cache has since expired again
- **THEN** an old-generation continuation returns 410 cursor_expired before another account refresh, even if Jira is currently unavailable

#### Scenario: New profile loses its timezone
- **WHEN** a valid old search cursor encounters a successfully refreshed profile whose timezone is now unavailable and whose generation has advanced
- **THEN** the continuation returns 410 cursor_expired while a new time-bounded search returns 503 timezone_unavailable

#### Scenario: Cache refresh does not change context
- **WHEN** a time-bounded continuation refreshes an expired snapshot with unchanged identity and timezone
- **THEN** it can continue with its original expiry and query rather than invalidating the cursor merely because TTL elapsed

#### Scenario: No-time search uses an already detected change
- **WHEN** another operation has detected an account-context change and a search cursor without caller time bounds contains the older generation
- **THEN** that continuation returns 410 cursor_expired without requiring another /myself request

#### Scenario: Comment pagination after timezone change
- **WHEN** a comment cursor remains valid under its existing project, parent, operation, configuration and expiry checks but account timezone has changed
- **THEN** timezone generation alone does not expire the comment cursor and normal visibility checks still run

## ADDED Requirements

### Requirement: Caller time offsets determine absolute search bounds
updatedFrom and updatedTo SHALL be either both absent or both nonempty RFC3339 timestamps with explicit offsets or Z. The gateway SHALL parse the supplied offsets before checking to greater than from and a maximum elapsed span of 30 days. Invalid bounds SHALL return the existing 400 invalid_parameter before account-context retrieval. Searches without caller time bounds SHALL NOT acquire account timezone solely for formatting. Neither host timezone nor an inferred caller timezone SHALL supply missing offsets.

#### Scenario: Caller omits a timezone
- **WHEN** updatedFrom or updatedTo has a time component but lacks Z or an explicit offset
- **THEN** the gateway returns 400 invalid_parameter without fetching account context or sending the business search

#### Scenario: Different offsets represent the same instant
- **WHEN** valid ranges use +08:00, Z or -04:00 representations of the same two absolute instants
- **THEN** range validation and the generated time clauses are identical for the same Jira account context

#### Scenario: Comparison requires offset-aware ordering
- **WHEN** the textual date/time order differs from the absolute instant order because the two bounds use different offsets
- **THEN** validation uses their absolute instant order and elapsed duration

### Requirement: JQL time literals use the authenticated account timezone
For a valid time-bounded search the gateway SHALL resolve the authenticated Jira account's timezone through the shared account-context provider and format both absolute bounds in that timezone. It SHALL retain the existing yyyy-MM-dd HH:mm minute precision and inclusive updated greater-than-or-equal / less-than-or-equal operators, dropping seconds and fractional seconds from the formatted literals. The clauses SHALL retain the existing literal escaping, fixed project scoping, configured filter conjunction and controlled ordering. A UTC account SHALL remain supported without a different caller contract. Configured filterJql dates SHALL remain Jira-evaluated expressions and SHALL NOT be reinterpreted as caller-offset timestamps.

#### Scenario: Shanghai account receives Shanghai input
- **WHEN** updatedFrom is 2026-09-29T10:00:45.123+08:00 and account timeZone is Asia/Shanghai with a valid later bound
- **THEN** the lower clause is updated >= "2026-09-29 10:00" rather than the UTC wall time 02:00

#### Scenario: UTC account receives Shanghai input
- **WHEN** the same updatedFrom is used with an account in UTC
- **THEN** the lower clause is updated >= "2026-09-29 02:00"

#### Scenario: Conversion crosses a date boundary
- **WHEN** updatedFrom is 2026-09-29T00:00:00+08:00 and the account timezone is UTC
- **THEN** the lower clause uses 2026-09-28 16:00 and preserves the represented date transition

#### Scenario: Account uses a non-hour offset
- **WHEN** updatedFrom is 2026-09-29T02:00:00Z and the account timezone is Asia/Kolkata
- **THEN** the lower clause uses 2026-09-29 07:30

### Requirement: Timezone rules and ambiguous local boundaries are handled explicitly
The deployed binary SHALL include IANA timezone data usable without a host/container zoneinfo installation. Conversion SHALL use the rules applicable to each bound's date, not a cached current numeric offset. Before sending time clauses, the gateway SHALL detect whether either final formatted local-minute boundary occurs more than once under distinct UTC offsets during a backward timezone transition; distinct seconds within an ordinary minute SHALL NOT count as this ambiguity. Such an ambiguous range SHALL return 400 ambiguous_time_range without executing the business search. Detection SHALL account for non-one-hour transitions and SHALL NOT rely only on a single local-time parse round trip. Crossing a timezone transition with unique endpoint minutes SHALL remain supported.

#### Scenario: Runtime lacks system zoneinfo
- **WHEN** the binary runs in the existing minimal runtime environment without installed system timezone data
- **THEN** Asia/Shanghai, Asia/Kolkata and America/New_York can be resolved using the embedded rules

#### Scenario: Query uses a historical seasonal offset
- **WHEN** the account timezone is America/New_York and the query dates have different seasonal rules from the current date
- **THEN** each bound uses its own date's timezone offset

#### Scenario: Repeated New York minute
- **WHEN** a bound in America/New_York formats to 2026-11-01 01:30 from either the -04:00 or -05:00 occurrence
- **THEN** the range is rejected as ambiguous_time_range rather than choosing either occurrence silently

#### Scenario: Half-hour rollback
- **WHEN** a bound falls in a repeated local minute during an Australia/Lord_Howe thirty-minute backward transition
- **THEN** the range is rejected by the same ambiguity rule

#### Scenario: Unique boundaries across a transition
- **WHEN** a valid range crosses a DST transition but neither formatted endpoint minute is repeated
- **THEN** the gateway can generate the account-local clauses without rejecting the entire transition day

### Requirement: Account timezone adaptation does not rewrite resource timestamps
The gateway SHALL preserve accepted caller-provided date/time string values when creating an issue, including their original timezone suffix, subject to existing field/schema/default checks. It SHALL preserve the Jira-provided strings for issue.updated, supported custom date/time fields and comment createdAt/updatedAt. Pure date values and date-looking text inside descriptions/comments SHALL NOT undergo timezone conversion. System created and updated SHALL remain prohibited as caller-supplied create fields. These rules SHALL NOT expand supported metadata types or promise that Jira accepts timezone-less date/time fields.

#### Scenario: Create value contains an explicit offset
- **WHEN** an allowed supported string date/time custom field contains 2026-09-29T10:00:45.123+08:00
- **THEN** the upstream create request contains the exact string regardless of the account timezone

#### Scenario: Read response contains a Jira offset
- **WHEN** Jira returns a supported timestamp string ending in +0800, +08:00 or Z
- **THEN** the gateway preserves that string while applying the existing field projection

#### Scenario: Caller supplies a system creation timestamp
- **WHEN** an issue creation includes created or updated in fields
- **THEN** it is rejected under the existing field boundary before an issue write
