//go:build e2e
// +build e2e

/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

func TestManagerDeploymentRefreshCommandsRestartAndWaitForRollout(t *testing.T) {
	cmds := managerDeploymentRefreshCommands(
		"postgres-operator-system",
		"postgres-operator-controller-manager",
	)

	if len(cmds) != 2 {
		t.Fatalf("expected two rollout commands, got %d", len(cmds))
	}

	wantRestart := []string{
		"kubectl",
		"-n", "postgres-operator-system",
		"rollout", "restart",
		"deployment/postgres-operator-controller-manager",
	}
	if got := cmds[0].Args; !reflect.DeepEqual(got, wantRestart) {
		t.Fatalf("restart command mismatch\nwant: %#v\n got: %#v", wantRestart, got)
	}

	wantStatus := []string{
		"kubectl",
		"-n", "postgres-operator-system",
		"rollout", "status",
		"deployment/postgres-operator-controller-manager",
		"--timeout=180s",
	}
	if got := cmds[1].Args; !reflect.DeepEqual(got, wantStatus) {
		t.Fatalf("rollout status command mismatch\nwant: %#v\n got: %#v", wantStatus, got)
	}
}

func TestManagerImageMatchesInstallManifests(t *testing.T) {
	kustomizationRaw, err := os.ReadFile(repoPath(t, "config", "manager", "kustomization.yaml"))
	if err != nil {
		t.Fatalf("read manager kustomization: %v", err)
	}
	var kustomization struct {
		Images []struct {
			Name    string `json:"name"`
			NewName string `json:"newName"`
			NewTag  string `json:"newTag"`
		} `json:"images"`
	}
	if err := yaml.Unmarshal(kustomizationRaw, &kustomization); err != nil {
		t.Fatalf("parse manager kustomization: %v", err)
	}
	var kustomizeImage string
	for _, image := range kustomization.Images {
		if image.Name == "controller" {
			kustomizeImage = fmt.Sprintf("%s:%s", image.NewName, image.NewTag)
			break
		}
	}
	if kustomizeImage == "" {
		t.Fatal("config/manager/kustomization.yaml must declare controller image")
	}
	if managerImage != kustomizeImage {
		t.Fatalf("managerImage = %q, want config/manager controller image %q", managerImage, kustomizeImage)
	}

	installRaw, err := os.ReadFile(repoPath(t, "dist", "install.yaml"))
	if err != nil {
		t.Fatalf("read dist/install.yaml: %v", err)
	}
	if !strings.Contains(string(installRaw), "image: "+managerImage) {
		t.Fatalf("dist/install.yaml must contain manager image %q", managerImage)
	}
}

func repoPath(t *testing.T, elems ...string) string {
	t.Helper()
	parts := append([]string{"..", ".."}, elems...)
	return filepath.Clean(filepath.Join(parts...))
}
