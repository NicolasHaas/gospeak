package gospeak_test

import (
	"slices"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestReleaseWaitsForContainerPublication(t *testing.T) {
	var workflow struct {
		Jobs struct {
			Release struct {
				Needs []string `yaml:"needs"`
				If    string   `yaml:"if"`
			} `yaml:"release"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(readProjectFile(t, ".github/workflows/release.yml")), &workflow); err != nil {
		t.Fatal(err)
	}
	release := workflow.Jobs.Release
	if !slices.Contains(release.Needs, "container") {
		t.Fatal("GitHub Release can publish before the scanned container image")
	}
	if release.If != "" {
		t.Fatal("release must retain default successful-dependencies gating")
	}
}
