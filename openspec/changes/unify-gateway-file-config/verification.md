# Implementation verification — 2026-09-14

Implemented the unified JSON contract and retained the existing routes, response assertions, authorization boundaries, Jira origin/attachment protections and FIFO behavior. All upstream interactions used disposable credentials and local mocks. No production upstream, deployment, caller repository or real configuration was used.

## Commands and results

The host Go executable was discovered in the existing container layer at `/opt/cache/containers/storage/overlay/34bb6a4e3763f3cdee1f4aa779f9383d3f1f6cf5e4b7e3970add7026d622043f/diff/usr/local/go/bin/go` (Go 1.27.1). Commands below used that executable on the host; the image builder independently used Go 1.23.

| Command / check | Result |
|---|---|
| `go test ./...` | All packages pass, including retained API assertions and the new application configuration tests. |
| `go vet ./...` | Pass. |
| `go test -race ./cmd/gateway ./internal/config ./internal/strictjson ./internal/auditlog ./internal/auth ./internal/jira ./internal/httpapi ./internal/queue` | Pass. |
| `go test -race ./internal/cklogs ./internal/httpapi` | Pass after the final literal-value preservation adjustment. |
| `node --test scripts/deploy/*.test.mjs` | 9 tests pass: generic environment delivery, removed CLI rejection, launcher cwd/path/probe/disabled audit, shell parsing and both merged app-stack overlays. |
| `docker build -t platform-gateway:config-smoke .` | Pass with the repository Dockerfile / Go 1.23; runtime UID 65532. |
| `python3 scripts/test/build-context-smoke.py` | Pass: sentinel root and nested config.json absent from actual builder context; Go 1.23 tests and vet pass. Existing Git/Docker exclusion patterns retained. |
| `python3 scripts/test/jira-container-smoke.py` | Pass: TLS context path, direct Jira despite proxy variables, CKLogs HTTP proxy and NO_PROXY bypass, UID 65532, read-only mount, missing direct bind rejection, disabled audit, cross-project/service denial, stale token denial and atomic replacement/recreation. |
| `python3 scripts/test/jira-compose-smoke.py` | Pass for self/install/149 × Bearer/Basic: actual container environment retains proxy/cert/unrelated/stale values; JSON retains credential punctuation; configured port 9123, read-only source, UID and rotation verified. Missing source is rejected by standalone preflight; engine limitation below. |
| Real `standalone.sh --mode host --app-root <temporary-dir> --config-file chosen.json` | Pass: built and launched the real binary, health/ready succeeded on an available non-default loopback port, relative path selected correctly, stale old values ignored, no audit directory created. Disposable process terminated afterward. |
| JSON example and fenced JSON blocks in README/gateway-auth/api | Parsed successfully. Deployment instruction scan leaves old setting names only in the intentional migration mapping. |
| `openspec validate unify-gateway-file-config --strict` | Pass. |
| `openspec validate --specs --strict` | All three prerequisite main capabilities pass. Informational length notices only. |
| `git diff --check` | Pass. |

## Behavioral coverage

- Strict parsing rejects unknown/duplicate keys, typed nulls, invalid types, files larger than 1 MiB and existing invalid token/project policies. Missing files fail with sanitized errors. Disabled services waive only absent required upstream fields.
- GATEWAY_CONFIG_FILE and default working-directory config.json are tested. Stale URL, file-selector and inbound-token environment values neither override nor block loading; an actual authorization request with the stale token returns 401.
- Jira-only, CKLogs-only and combined configurations work. Both upstream auth modes are exercised through TLS search requests at `/jira/rest/api/2/search`, with CS and IT sharing one client. Mixed/incomplete credentials and CR/LF fail; punctuation/whitespace is preserved.
- Missing fields receive defaults, while queue size 0 and audit false survive. Fractional/negative/overflow times fail. timeoutMs 9223372031854 is accepted with positive HTTP timeout equal to the duration plus five seconds; the next value is rejected. waitTimeoutMs is checked at 9223372036854 and the next value. Runtime integer maxima are accepted without float conversion. Arbitrary createDefaults numbers remain json.Number, with existing lossy-number rejection retained.
- Linux audit tests execute as UID/GID 65532 even when invoked by root. Unwritable directories/current-day files fail; existing file bytes survive initialization and no probe file or synthetic record remains. Disabled audit has no writer side effects.
- Deployment code has no old-variable reads, rejection lists or warnings. The static Compose `!reset` declarations remove dedicated inherited mappings without inspecting environment values; env_file delivery remains intact. No old host mount selector is read.

## Environment limitation

Here `docker` is Podman 5.4.2 and `docker-compose` is 2.26.1 using Podman's Docker API. Direct `docker run --mount` rejects a missing source. Through this Compose/API combination, however, a missing source can become a directory even with `bind.create_host_path: false`. The smoke reports this limitation explicitly. All three supported standalone deployment paths now validate a readable regular source before invoking the engine, and that failure path is verified. Native Docker Engine's Compose missing-source behavior was not exercised here; the declarative protection remains in the Compose files.

The container scripts accept `-` (default) for the local engine or a Docker context as their first argument. They create and remove only temporary fixtures/containers. The local smoke image remains available for inspection. No live Jira version/field/permission acceptance or production rollout is claimed.

## Specification handoff

Task 5.1 archived the completed prerequisite under `archive/2026-09-14-add-project-scoped-jira-gateway`. Its three ADDED capabilities were copied into main specs; every requirement block and every archived source artifact was verified unchanged. Main-spec purposes were supplied instead of placeholders.

This active change now includes complete MODIFIED blocks for `gateway-file-auth` file selection and obsolete inbound environment behavior. The original scenarios are retained with the changed startup result. `gateway-application-config` is the single new application contract. Archiving this change will apply those two updates together with that new capability; this apply operation intentionally leaves the current change ready for archive.
