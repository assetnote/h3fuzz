## Summary

Describe the problem and the approach taken. Link any relevant design or protocol documentation.

Closes #

## Validation

List the commands you ran and any manual CLI, web-console, or local-harness checks. Include before-and-after measurements for performance changes.

```text
# Commands and results
```

## Checklist

- [ ] The branch is based on the latest `main`.
- [ ] Relevant Go and frontend tests pass.
- [ ] Go changes are formatted with `gofmt`; applicable `go vet` and race tests pass.
- [ ] UI changes were checked in the dark theme at narrow and wide viewport sizes.
- [ ] User-facing behavior and flags are documented.
- [ ] Generated assets and characterization goldens are updated when their sources change.
- [ ] No binaries, credentials, scan databases, benchmark artifacts, or target data are included.
- [ ] New dependencies and copied code have compatible licenses and attribution.

See [CONTRIBUTING.md](https://github.com/assetnote/h3fuzz/blob/main/CONTRIBUTING.md) for build and validation guidance. Report vulnerabilities through the private process in [SECURITY.md](https://github.com/assetnote/h3fuzz/blob/main/SECURITY.md), not in a pull request.
