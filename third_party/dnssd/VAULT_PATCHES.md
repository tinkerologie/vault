# Vault patches to brutella/dnssd

This directory is `github.com/brutella/dnssd` **v1.2.14**, copied verbatim
from the module cache (MIT licence, see `LICENSE`). The root `go.mod` selects
it with:

```text
replace github.com/brutella/dnssd => ./third_party/dnssd
```

Do not reformat or lint-fix the upstream files. `.pre-commit-config.yaml`,
`gosec` (Makefile) and CodeQL skip `third_party/`, so that the upstream code
stays easy to diff and re-sync.

## Patch 1: scope query answers to the service's interfaces

- **Vault issue:** tinkerologie/vault#498
- **Upstream pull request:** not opened yet
- **File:** `responder.go`, `handleQuery`, marked `Vault patch`
- **Test:** `responder_iface_test.go`
  (`TestQueryOnExcludedInterfaceIsNotAnswered`,
  `TestQueryForUnscopedServiceIsAnsweredEverywhere`,
  `TestScopedAndUnscopedServicesOnExcludedInterface`)

The responder joins every multicast interface. Announcements honour
`Service.Ifaces`, and so do the A, AAAA and NSEC helpers in `dns.go`. But
`handleQuery` answered PTR, SRV, TXT and `_services._dns-sd._udp` meta queries
for every service, whichever interface the query arrived on. A host on another
local link that queried `_vault._tcp` was therefore told about a service that
was meant for one interface only. The patch skips a service in `handleQuery`
when `!srv.IsVisibleAtInterface(req.IfaceName())`. A service with an empty
`Ifaces` list is still visible on every interface.

Run the tests with `make test-third-party`. CI runs them as well.

## Removing this copy

When an upstream release contains the fix:

1. Bump `github.com/brutella/dnssd` in the root `go.mod`.
2. Remove the `replace` directive and run `go mod tidy`.
3. Delete `third_party/dnssd/`.
4. Remove the `COPY third_party/dnssd/` line from `Dockerfile`, the
   `test-third-party` target in `Makefile`, the third_party test step in
   `.github/workflows/ci.yml`, and the `third_party` exclusions in
   `.pre-commit-config.yaml`, the `gosec` command and
   `.github/codeql/codeql-config.yml`.
5. Remove the comment above `newResponder()` in
   `internal/discovery/discovery.go`.
