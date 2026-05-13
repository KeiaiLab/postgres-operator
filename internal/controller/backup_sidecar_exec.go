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
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

// BackupSidecarTarget 은 pgBackRest sidecar 명령을 실행할 Pod/container 위치다.
type BackupSidecarTarget struct {
	Namespace string
	Pod       string
	Container string
}

// BackupSidecarExecutor 는 K8s pod exec 실행 지점이다.
type BackupSidecarExecutor interface {
	Exec(ctx context.Context, target BackupSidecarTarget, command []string) ([]byte, error)
}

// KubernetesBackupSidecarExecutor 는 client-go remotecommand 기반 production executor다.
type KubernetesBackupSidecarExecutor struct {
	Config *rest.Config
	Client kubernetes.Interface
}

// NewKubernetesBackupSidecarExecutor 는 manager rest config 로 sidecar executor 를 만든다.
func NewKubernetesBackupSidecarExecutor(config *rest.Config) (*KubernetesBackupSidecarExecutor, error) {
	if config == nil {
		return nil, errors.New("backup sidecar executor requires rest config")
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	return &KubernetesBackupSidecarExecutor{
		Config: rest.CopyConfig(config),
		Client: clientset,
	}, nil
}

// Exec 은 target Pod/container 에 command argv 를 전달하고 stdout 을 반환한다.
func (e *KubernetesBackupSidecarExecutor) Exec(
	ctx context.Context,
	target BackupSidecarTarget,
	command []string,
) ([]byte, error) {
	if e == nil || e.Config == nil || e.Client == nil {
		return nil, errors.New("backup sidecar executor is not configured")
	}
	if target.Namespace == "" || target.Pod == "" || target.Container == "" {
		return nil, fmt.Errorf("invalid backup sidecar target: %+v", target)
	}
	if len(command) == 0 {
		return nil, errors.New("backup sidecar command is empty")
	}

	req := e.Client.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(target.Namespace).
		Name(target.Pod).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: target.Container,
			Command:   command,
			Stdout:    true,
			Stderr:    true,
		}, clientgoscheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(e.Config, http.MethodPost, req.URL())
	if err != nil {
		return nil, err
	}

	var stdout, stderr bytes.Buffer
	if err := executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	}); err != nil {
		return nil, fmt.Errorf("backup sidecar exec failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}
