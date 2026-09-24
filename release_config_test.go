package gospeak_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func readProjectFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path) //nolint:gosec // Tests pass fixed repository fixture paths.
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(contents)
}

func TestCIUsesImmutableToolingAndSupportsManualRuns(t *testing.T) {
	workflow := readProjectFile(t, ".github/workflows/ci.yml")
	if !strings.Contains(workflow, "workflow_dispatch:") {
		t.Error("CI workflow has no manual trigger")
	}
	if strings.Contains(workflow, "version: latest") {
		t.Error("CI installs an unpinned tool version")
	}
	assertImmutableActionRefs(t, workflow)
}

func TestReleaseScansBeforePublishingVersionedArtifacts(t *testing.T) {
	workflow := readProjectFile(t, ".github/workflows/release.yml")
	assertImmutableActionRefs(t, workflow)
	for _, required := range []string{"group: release-publish", "git fetch --force --prune --prune-tags --tags origin", "IMAGE_TAG=", "${#IMAGE_TAG}", "image-tag="} {
		if !strings.Contains(workflow, required) {
			t.Errorf("release workflow missing %q", required)
		}
	}

	scan := strings.Index(workflow, "Trivy scan")
	push := strings.Index(workflow, "docker push")
	if scan < 0 || push < 0 || scan > push {
		t.Errorf("release must scan before publishing: scan index %d, push index %d", scan, push)
	}
	if !strings.Contains(workflow, "exit-code: \"1\"") {
		t.Error("release vulnerability scan is not configured to fail closed")
	}
	for _, variable := range []string{"pkg/version.tag", "pkg/version.commit", "pkg/version.date"} {
		if !strings.Contains(workflow, variable) {
			t.Errorf("release workflow does not inject %s", variable)
		}
	}
}

func TestContainerBuildAndDeploymentAreHardened(t *testing.T) {
	containerfile := readProjectFile(t, "Containerfile")
	if !strings.Contains(containerfile, "FROM golang:1.24.4-bookworm@sha256:") {
		t.Error("container build does not use the release Go toolchain version")
	}
	digest := regexp.MustCompile(`^[^@]+@sha256:[0-9a-f]{64}$`)
	for _, line := range strings.Split(containerfile, "\n") {
		if !strings.HasPrefix(line, "FROM ") {
			continue
		}
		base := strings.Fields(line)[1]
		if !strings.Contains(base, "/") && !strings.Contains(base, ":") {
			continue // a stage declared earlier in this Containerfile
		}
		if !digest.MatchString(base) {
			t.Errorf("container base is not pinned by digest: %s", line)
		}
	}
	for _, required := range []string{"EXPOSE 9600/tcp 9601/udp 9603/tcp", "PORTAUDIO_COMMIT=", "OPUS_COMMIT="} {
		if !strings.Contains(containerfile, required) {
			t.Errorf("Containerfile missing %q", required)
		}
	}
	for _, required := range []string{"CGO_ENABLED=0 go build -o /out/gospeak-server", "FROM scratch AS server"} {
		if !strings.Contains(containerfile, required) {
			t.Errorf("static server image missing %q", required)
		}
	}
	if strings.Contains(containerfile, "ca-certificates") {
		t.Error("server runtime includes an unused CA bundle")
	}
	sourceCommit := regexp.MustCompile(`(?m)^ARG (PORTAUDIO|OPUS)_COMMIT=[0-9a-f]{40}$`)
	if matches := sourceCommit.FindAllString(containerfile, -1); len(matches) != 2 {
		t.Errorf("Containerfile has %d immutable native source commits, want 2", len(matches))
	}

	for _, path := range []string{"compose.yaml", "deploy/compose.yaml"} {
		compose := readProjectFile(t, path)
		for _, required := range []string{"cap_drop:", "- ALL", "no-new-privileges:true"} {
			if !strings.Contains(compose, required) {
				t.Errorf("%s missing %q", path, required)
			}
		}
	}

	dockerignore := readProjectFile(t, ".dockerignore")
	for _, excluded := range []string{
		"**", "**/*.crt", "**/*.db*", "**/*.key", "**/*.token",
	} {
		if !strings.Contains(dockerignore, excluded) {
			t.Errorf(".dockerignore does not exclude %q", excluded)
		}
	}
}

func assertImmutableActionRefs(t *testing.T, workflow string) {
	t.Helper()
	actionRef := regexp.MustCompile(`(?m)^\s*-?\s*uses:\s*[^#\s]+@([^\s#]+)`)
	sha := regexp.MustCompile(`^[0-9a-f]{40}$`)
	matches := actionRef.FindAllStringSubmatch(workflow, -1)
	if len(matches) == 0 {
		t.Fatal("workflow contains no action references")
	}
	for _, match := range matches {
		if !sha.MatchString(match[1]) {
			t.Errorf("action reference is mutable: %s", match[0])
		}
	}
}
