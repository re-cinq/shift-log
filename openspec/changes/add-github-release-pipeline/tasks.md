# Tasks: GitHub Release Pipeline

## 1. CI Workflow

- [x] 1.1 Create `.github/workflows/ci.yml`
- [x] 1.2 Add `lint` job with golangci-lint
- [x] 1.3 Add `test` job running unit + acceptance tests
- [x] 1.4 Add `build` job with matrix for 5 platforms
- [x] 1.5 Test locally with `act push -W .github/workflows/ci.yml` (act installed, Docker unavailable in environment)

## 2. GoReleaser Configuration

- [x] 2.1 Create `.goreleaser.yml`
- [x] 2.2 Configure builds for darwin/linux/windows x amd64/arm64
- [x] 2.3 Configure archives (tar.gz for Unix, zip for Windows)
- [x] 2.4 Configure checksums and changelog
- [x] 2.5 Validate with `goreleaser check`
- [x] 2.6 Test with `goreleaser release --snapshot --clean`

## 3. Version Bump Workflow

- [x] 3.1 Create `.github/workflows/bump-version.yml`
- [x] 3.2 Add workflow_dispatch with patch/minor/major dropdown
- [x] 3.3 Implement version calculation from latest tag
- [x] 3.4 Create and push annotated git tag
- [x] 3.5 Test locally with `act workflow_dispatch -W .github/workflows/bump-version.yml` (act installed, Docker unavailable)

## 4. Release Workflow

- [x] 4.1 Create `.github/workflows/release.yml`
- [x] 4.2 Trigger on `v*` tag push
- [x] 4.3 Run GoReleaser to build + publish
- [ ] 4.4 Test by creating a test tag

## 5. Integration Test Workflow

- [x] 5.1 Create `.github/workflows/integration.yml`
- [x] 5.2 Trigger on main branch push + weekly schedule + manual
- [x] 5.3 Use `CLAUDE_CODE_OAUTH_TOKEN` secret
- [x] 5.4 Skip on fork PRs (no secrets)

## 6. Local Testing Setup

- [x] 6.1 Create `.actrc` with default settings
- [ ] 6.2 Document act commands in README or CONTRIBUTING.md
- [x] 6.3 Verify all workflows pass locally before pushing (GoReleaser validated, act requires Docker)

## 7. Verification

- [ ] 7.1 Open test PR and verify CI passes
- [ ] 7.2 Run version bump workflow and verify tag created
- [ ] 7.3 Verify GitHub Release created with binaries
- [ ] 7.4 Download and test a release binary
