/*
Copyright 2026 Keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"strings"
	"testing"

	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

func TestNewKubernetesBackupSidecarExecutorRequiresConfig(t *testing.T) {
	t.Parallel()

	executor, err := NewKubernetesBackupSidecarExecutor(nil)
	if err == nil {
		t.Fatalf("NewKubernetesBackupSidecarExecutor(nil) err = nil, want error")
	}
	if executor != nil {
		t.Fatalf("executor = %#v, want nil", executor)
	}
}

func TestKubernetesBackupSidecarExecutorRejectsMisconfiguredExecutor(t *testing.T) {
	t.Parallel()

	var executor *KubernetesBackupSidecarExecutor
	_, err := executor.Exec(context.Background(), BackupSidecarTarget{
		Namespace: "default",
		Pod:       "postgres-0",
		Container: "postgres",
	}, []string{"pgbackrest", "backup"})
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("Exec err = %v, want not configured", err)
	}
}

func TestKubernetesBackupSidecarExecutorRejectsInvalidTarget(t *testing.T) {
	t.Parallel()

	executor := &KubernetesBackupSidecarExecutor{
		Config: &rest.Config{},
		Client: fake.NewClientset(),
	}
	_, err := executor.Exec(context.Background(), BackupSidecarTarget{
		Namespace: "default",
		Pod:       "",
		Container: "postgres",
	}, []string{"pgbackrest", "backup"})
	if err == nil || !strings.Contains(err.Error(), "invalid backup sidecar target") {
		t.Fatalf("Exec err = %v, want invalid target", err)
	}
}

func TestKubernetesBackupSidecarExecutorRejectsEmptyCommand(t *testing.T) {
	t.Parallel()

	executor := &KubernetesBackupSidecarExecutor{
		Config: &rest.Config{},
		Client: fake.NewClientset(),
	}
	_, err := executor.Exec(context.Background(), BackupSidecarTarget{
		Namespace: "default",
		Pod:       "postgres-0",
		Container: "postgres",
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "command is empty") {
		t.Fatalf("Exec err = %v, want empty command", err)
	}
}
