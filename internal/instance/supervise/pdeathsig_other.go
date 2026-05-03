//go:build !linux

/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package supervise

import (
	osexec "os/exec"
	"syscall"
)

// setupChildProcessAttrs 는 non-Linux 환경 (개발자 macOS 등) 에서 no-op.
// SysProcAttr 의 Pdeathsig 필드가 Linux 전용이므로, 다른 OS 에서는 prctl 보호
// 없이 단순 fork 한다. 운영 Pod 는 Linux 컨테이너이므로 production 영향 없음.
func setupChildProcessAttrs(_ *osexec.Cmd, _ syscall.Signal) {}
