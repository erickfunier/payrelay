## Summary

<!-- What changes, and why? -->

## Safety and API impact

- [ ] This keeps PayRelay mock-only; it does not add real payment processing or card-data handling.
- [ ] I documented any API, configuration, or migration change.

## Verification

- [ ] `go vet ./...`
- [ ] `go test ./...`
- [ ] `go build ./cmd/server`

## Checklist

- [ ] The branch targets `develop` (or targets `master` only for a release/hotfix).
- [ ] Tests and documentation are updated where applicable.
- [ ] New or updated tests use Given / When / Then names and table-driven cases where inputs share a behavior.
