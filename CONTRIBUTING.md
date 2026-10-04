# Contributing to talon

Thanks for considering a contribution.

## Reporting bugs

Use the bug report template. Include the exact error output and the steps to
reproduce — a minimal reproduction is worth more than a long description.

## Sending a pull request

1. Fork the repository and create a branch off `main` with a descriptive name.
2. Make the change. Keep the scope tight; unrelated refactors are hard to review.
3. Verify:

   ```sh
   go build ./...
   go test ./...
   go vet ./...
   ```

4. Open the PR and fill in the template.

## Security-sensitive changes

talon gates sensitive data and restricts network access. If your change touches
`internal/secrets`, the network policy, or the sandbox, say so explicitly in the
PR description and explain why the existing guarantees still hold.

## Security reports

Do **not** open a public issue for a vulnerability. Use GitHub's private
vulnerability reporting on the Security tab of this repository.

## Code style

Follow the surrounding code: `gofmt`, standard library first, comments for
non-obvious reasoning rather than restating the code.
