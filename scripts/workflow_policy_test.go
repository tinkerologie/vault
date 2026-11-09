package main

import (
	"regexp"
	"strings"
	"testing"
)

// Release and CI trust boundaries (#537, #560). The workflows cannot run
// locally, so these checks pin the properties that must survive later edits.

// workflowJob returns the text of one job from a workflow file.
func workflowJob(t *testing.T, workflow, job string) string {
	t.Helper()
	_, jobs, found := strings.Cut(workflow, "\njobs:\n")
	if !found {
		t.Fatal("workflow has no jobs section")
	}
	_, rest, found := strings.Cut("\n"+jobs, "\n  "+job+":\n")
	if !found {
		t.Fatalf("workflow has no %q job", job)
	}
	if loc := regexp.MustCompile(`(?m)^  [a-zA-Z0-9_-]+:$`).FindStringIndex(rest); loc != nil {
		return rest[:loc[0]]
	}
	return rest
}

func assertOrder(t *testing.T, text string, earlier, later string) {
	t.Helper()
	first, second := strings.Index(text, earlier), strings.Index(text, later)
	if first < 0 || second < 0 || first > second {
		t.Errorf("%q must appear before %q", earlier, later)
	}
}

func TestReleaseValidatesTagBeforeRunningItsCode(t *testing.T) {
	workflow := readRepoFile(t, ".github/workflows/release.yml")
	validate := workflowJob(t, workflow, "validate-ref")
	for _, want := range []string{"ref: main", "persist-credentials: false", "merge-base --is-ancestor", "refs/tags/"} {
		if !strings.Contains(validate, want) {
			t.Errorf("validate-ref job is missing %q", want)
		}
	}
	for _, gone := range []string{"make ", "npm ", "go build", "go test", "secrets."} {
		if strings.Contains(validate, gone) {
			t.Errorf("validate-ref job must not contain %q", gone)
		}
	}

	build := workflowJob(t, workflow, "release")
	if !strings.Contains(build, "needs: validate-ref") {
		t.Error("release job must need validate-ref")
	}
	if !strings.Contains(build, "ref: ${{ needs.validate-ref.outputs.sha }}") {
		t.Error("release job must check out the validated commit, not a movable name")
	}
}

func TestReleaseTokenNeverMeetsReleaseRefCode(t *testing.T) {
	workflow := readRepoFile(t, ".github/workflows/release.yml")
	if strings.Contains(workflow, "x-access-token") {
		t.Error("release workflow must not put a token in a git URL")
	}

	build := workflowJob(t, workflow, "release")
	for _, gone := range []string{"TINKER_RELEASE", "create-github-app-token", "PLG_PUSH_TOKEN"} {
		if strings.Contains(build, gone) {
			t.Errorf("job that runs release-ref code must not contain %q", gone)
		}
	}

	publish := workflowJob(t, workflow, "publish-plg")
	if !strings.Contains(publish, "ref: main") {
		t.Error("publish-plg must check out main")
	}
	if !strings.Contains(publish, "persist-credentials: false") {
		t.Error("publish-plg must not persist checkout credentials")
	}
	if !strings.Contains(publish, "environment: release") {
		t.Error("publish-plg must run in the release environment")
	}
	for _, gone := range []string{"build/plgnotes", "make ", "npm ", "go test", "needs.validate-ref.outputs.sha"} {
		if strings.Contains(publish, gone) {
			t.Errorf("publish-plg must not contain %q", gone)
		}
	}
	// The token exists only after the last code from the checkout has run.
	assertOrder(t, publish, "go build", "create-github-app-token")
	assertOrder(t, publish, `"$RUNNER_TEMP/plgnotes" update-plg`, "create-github-app-token")
	assertOrder(t, publish, "create-github-app-token", "gh auth setup-git")
	if got := strings.Count(publish, "steps.release-token.outputs.token"); got != 1 {
		t.Errorf("the token must reach exactly one step, found %d references", got)
	}
}

// A PR merging while publish-plg runs must not lose the release's PLG update.
// A retry regenerates it on the new main, so a version bump fails the helper's
// version check instead of pairing the new version with this release's hash.
// Nothing fetched is run, and the regeneration never sees the token.
func TestPLGPushRegeneratesOnNewMainWithoutRunningFetchedCode(t *testing.T) {
	publish := workflowJob(t, readRepoFile(t, ".github/workflows/release.yml"), "publish-plg")
	beforeMint, afterMint, found := strings.Cut(publish, "create-github-app-token")
	if !found {
		t.Fatal("publish-plg does not mint a token")
	}
	const regenerate = `env -u GH_TOKEN bash "$RUNNER_TEMP/update-plg.sh"`
	if !strings.Contains(beforeMint, `> "$RUNNER_TEMP/update-plg.sh"`) {
		t.Error("the update script must be written outside the checkout before the token exists")
	}
	assertOrder(t, afterMint, "git push origin main", "git reset --hard FETCH_HEAD")
	assertOrder(t, afterMint, "git reset --hard FETCH_HEAD", regenerate)
	if strings.Contains(afterMint, "git pull") || strings.Contains(afterMint, "git rebase") {
		t.Error("a replayed commit can pair a new manifest version with this release's hash")
	}
	for _, gone := range []string{"go build", "go run", "make ", "npm ", "python3", "./"} {
		if strings.Contains(afterMint, gone) {
			t.Errorf("steps holding the token must not run %q", gone)
		}
	}
}

func TestCILintVersionIsPinned(t *testing.T) {
	job := workflowJob(t, readRepoFile(t, ".github/workflows/ci.yml"), "test")
	const install = "go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@"
	pin := regexp.MustCompile(`(?m)` + regexp.QuoteMeta(install) + `v\d+\.\d+\.\d+$`)
	if got := strings.Count(job, install); got != 1 || !pin.MatchString(job) {
		t.Fatal("the test job must install one exact golangci-lint version from Go modules")
	}
	assertOrder(t, job, install, "golangci-lint-action@")
	lintSteps := regexp.MustCompile(`(?m)golangci-lint-action@[0-9a-f]{40}[^\n]*\n(?:[^\n]*\n){0,3}[ \t]+install-mode: none$`)
	if got := strings.Count(job, "golangci-lint-action@"); got != 2 || len(lintSteps.FindAllString(job, -1)) != got {
		t.Fatal("both pinned lint actions must reuse the installed binary without downloading another copy")
	}
}

func TestCIOIDCIsIsolatedFromRepositoryCode(t *testing.T) {
	workflow := readRepoFile(t, ".github/workflows/ci.yml")
	if strings.Contains(workflowJob(t, workflow, "test"), "id-token") {
		t.Error("the job that runs repository code must not request id-token")
	}
	upload := workflowJob(t, workflow, "coverage-upload")
	if !strings.Contains(upload, "id-token: write") {
		t.Error("coverage-upload must hold the Codecov OIDC permission")
	}
	if regexp.MustCompile(`(?m)^\s+(- )?run:`).MatchString(upload) {
		t.Error("coverage-upload must not run shell steps")
	}
	if got := strings.Count(workflow, "codecov/codecov-action@"); got != strings.Count(upload, "codecov/codecov-action@") {
		t.Error("every Codecov upload must live in coverage-upload")
	}
}
