## 🚀 Quick Start

```shell
make init # downloads the Go modules, installs the git hooks and creates .env
```

The git hooks (lefthook) run `make lint_check` before a commit, and `make test` and `make integration_test` before a push (Docker needed, about 10 minutes). If `make init` reports that `core.hooksPath` is set, unset it: `git config --unset core.hooksPath` (add `--global` if it is set there).

### Test Locally

```bash
./test.sh # Single request test
make test             # unit tests
make integration_test # needs Docker; also builds the image and runs the e2e test (about 10 minutes)
make lint_check
```

To test the service under load:

```shell
docker compose up --build -d
python3 load_test.py --help # Load Testing
```

## Bump Version

To release a new version your [commit message should follow these rules](https://github.com/semantic-release/semantic-release?tab=readme-ov-file#commit-message-format) which is the default behavior of `semantic-release`.

> [!CAUTION]
>
> `feat!: some message` won't release a new major version. So make sure to use the correct commit message:
>
> ```cmd
> git commit -m "perf: some message" -m "BREAKING CHANGE: extra details"
> ```

### Automated Docker Hub release

This repository is configured to auto-release Docker images from `Dockerfile` using Conventional Commits.

- Workflow: `.github/workflows/dockerhub-release.yml`.
- Release config: `.releaserc.json` ([semantic-release](https://www.npmjs.com/package/semantic-release)). The version is passed to the image as the `SERVICE_VERSION` build argument.
- Trigger: push to `main`.

How versioning works:

| Commit type(s)                                           | Release behavior       |
| -------------------------------------------------------- | ---------------------- |
| `feat:`                                                  | **minor** version bump |
| `fix:` or `perf:`                                        | **patch** version bump |
| `chore:`, `docs:`, `style:`, `refactor:`, `test:`, `ci:` | No release by default  |

For each release, the workflow builds from `Dockerfile` and pushes:

- `9109679196/piper-tts-rest-api:<semantic-version>`
- `9109679196/piper-tts-rest-api:latest`

## Design & Code Philosophy

1. Simpler is better - do not overcomplicate.
2. Prefer early returns over nested conditionals.
3. Use Go's `testing` package with testify (`require` for preconditions, `assert` for checks).
   - Use [AAA (Arrange, Act, Assert) style of writing test](https://stackoverflow.com/tags/arrange-act-assert/info).
4. IMPORTANT: Avoid overly defensive programming; avoid insistence checks; only manage exceptions when necessary.
5. Use latest version of libraries and idiomatic approaches as of today.
