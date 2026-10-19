# Contributing to PayRelay

PayRelay is a public, mock-only payment-event platform. Contributions must preserve its safety boundary: do not add real payment processing, card-data collection, or live provider credentials.

## Branch model

| Branch | Purpose | Changes arrive through |
| --- | --- | --- |
| `master` | Production-ready release history | Release pull requests from `develop` |
| `develop` | Integration branch for the next release | Feature/fix/documentation pull requests |

Do not push directly to `master` or `develop`. Start each unit of work from the latest `develop` and open a pull request back to `develop`.

```sh
git switch develop
git pull --ff-only origin develop
git switch -c feat/short-description
```

Use one of these lower-case, slash-delimited prefixes:

- `feat/` — a product capability
- `fix/` — a defect correction
- `docs/` — documentation only
- `chore/` — maintenance, CI, or tooling
- `refactor/` — behavior-preserving code restructure
- `test/` — test-focused work

Examples: `feat/endpoint-secret-rotation`, `fix/retry-overflow`, and `docs/webhook-verification`.

## Pull requests

Every pull request must have a focused description, explain any API or security impact, and pass CI. Keep changes small enough to review. Rebase or merge the current `develop` branch before merge if GitHub reports conflicts.

Before opening a PR, run:

```sh
go vet ./...
go test ./...
go build ./cmd/server
```

Use a squash merge for feature branches so `develop` retains a readable history. Delete the source branch after merge.

## Releases

1. Ensure `develop` is green, documented, and ready to release.
2. Open a pull request with base `master` and compare branch `develop`.
3. Include the release version, key changes, upgrade notes, and test evidence in the PR.
4. Merge only after CI passes. Tag the resulting `master` commit and publish release notes.
5. If a production-only hotfix is necessary, branch from `master`, merge it back through a PR, then immediately open a follow-up PR to bring the same fix into `develop`.

## Local conventions

Keep secrets in `.env`, never commit database files or generated credentials, and preserve the mock-only safety guarantees in the README. Add tests for delivery, signing, retry, persistence, and API behavior whenever those areas change.

## Test conventions

Tests describe behavior in the **Given / When / Then** form. Name top-level tests as `Test<Component>_Given<State>_When<Action>_Then<Outcome>` and give subtests lower-case `given_..._when_..._then_...` names. This makes a failing test readable as a requirement without opening its implementation.

Prefer table-driven tests whenever multiple inputs exercise the same behavior. Keep the test case's input, action, and expected result together:

```go
cases := []struct {
	name string
	input string
	want  int
}{
	{name: "given_missing_key_when_creating_event_then_returns_bad_request", input: `{}`, want: http.StatusBadRequest},
}

for _, tt := range cases {
	t.Run(tt.name, func(t *testing.T) {
		// Given: tt.input
		// When: invoke the handler
		// Then: compare the response with tt.want
	})
}
```

Use a focused single-scenario test when a lifecycle needs ordered setup, such as creating an event, dispatching it, and verifying its delivery history.
