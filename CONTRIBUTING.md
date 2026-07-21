# Contributing to wacli

Thanks for your interest in improving wacli! This is a small, focused project;
contributions of all sizes are welcome.

## Development setup

Requirements: **Go 1.25+** and a C toolchain (CGO is required for the SQLite
driver `mattn/go-sqlite3`).

```bash
git clone https://github.com/MelloB1989/wacli
cd wacli
go build -o wacli .
```

Run the daemon against a throwaway state directory so you never touch a real
account's data:

```bash
WACLI_HOME=$(mktemp -d) ./wacli login
WACLI_HOME=$(mktemp -d) ./wacli daemon
```

## Before you open a PR

Please make sure the following pass:

```bash
gofmt -l .        # must print nothing
go vet ./...      # must be clean
go build ./...    # must succeed
go test ./...     # if you touched anything with tests
```

CI runs the same checks on every PR (see `.github/workflows/ci.yml`).

## Guidelines

- **Keep the CLI and the HTTP API in sync.** Every CLI subcommand is a thin
  client over a daemon endpoint — if you add one, add both, and document it in
  `docs/cli-reference.md` (and `docs/ai-harness-reference.md` if agents use it).
- **Don't break the JSON contract lightly.** AI harnesses (e.g. KARMAX) depend
  on the webhook payload and API response shapes. Additive changes are fine;
  renames/removals need a good reason and a note in the PR.
- **Never log or persist secrets.** No message contents in error logs beyond
  what's necessary; no credentials in code or fixtures.
- **Match the existing style.** Standard `gofmt`, small focused functions,
  errors wrapped with context.

## Reporting bugs & requesting features

Open an issue using the templates in `.github/ISSUE_TEMPLATE`. For anything
security-sensitive, follow [SECURITY.md](SECURITY.md) instead of filing a public
issue.

## License

By contributing, you agree that your contributions will be licensed under the
[MIT License](LICENSE).
