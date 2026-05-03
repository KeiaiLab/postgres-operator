/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package supervise 의 본 파일 (standby.go) 은 RFC 0006 R3 — PostgreSQL 표준
// `$PGDATA/standby.signal` 파일 lifecycle 헬퍼를 제공한다.
//
// PG 표준 의미론:
//   - standby.signal 존재 → postgres 가 standby 로 부팅 (WAL replay, read-only).
//   - 부재 → primary 로 부팅 (write 가능).
//
// 본 헬퍼는 controller 측 bootstrap init container (RFC 0006 R3 Task B) 와
// 계약 관계다 — bootstrap 이 pg_basebackup 으로 seed 한 경우 standby.signal 을
// 생성해 두고, instance manager 는 election 결과에 따라 다음과 같이 조작한다:
//
//   - OnStartedLeading: RemoveStandbySignal (primary 로 promote 직전).
//   - OnStoppedLeading: CreateStandbySignal (Pod 재시작 후 standby 로 부팅하도록).
//
// postgres 의존이 없는 순수 file ops 함수만 두어 테스트 격리가 용이하도록 설계
// — supervise.go / sql.go 와 분리한 이유.
//
// 알려진 알파 한계 (RFC 0007 후속): instance manager 가 panic / SIGKILL 으로
// 종료되어 OnStoppedLeading 이 실행되지 않으면 standby.signal 이 남지 않아 다음
// 부팅 시 옛 leader 가 primary 로 부팅 가능. PVC fence 가 promote 측에서 검사하므로
// 데이터 분기는 회피되나, 일시적 ambiguity 가 있다. 후속: bootstrap 이 PG_VERSION
// 존재 + clear-primary marker 부재 시 standby.signal 자동 생성.
package supervise

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// standbySignalFile 은 PG 가 인식하는 standby 모드 sentinel 파일명 (PG 12+).
const standbySignalFile = "standby.signal"

// IsStandby 는 dataDir 안에 standby.signal 파일이 존재하는지 검사한다.
// stat 오류가 ErrNotExist 이외인 경우에도 false 로 보수적 판정 (호출 측이
// boolean 만 보므로 별도 error 반환 없음 — promote 결정의 부수 게이트로만 쓰임).
func IsStandby(dataDir string) bool {
	_, err := os.Stat(filepath.Join(dataDir, standbySignalFile))
	if err == nil {
		return true
	}
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	// 그 외 stat 오류 (권한 등) — promote 결정의 부수 게이트라 false 반환.
	return false
}

// RemoveStandbySignal 은 dataDir 의 standby.signal 을 삭제한다. 이미 부재 시
// idempotent — nil 반환. 다른 IO 오류는 그대로 wrap 하여 반환한다.
func RemoveStandbySignal(dataDir string) error {
	path := filepath.Join(dataDir, standbySignalFile)
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

// CreateStandbySignal 은 dataDir 에 빈 standby.signal 파일 (mode 0600) 을 생성
// 한다. 이미 존재하면 그대로 둔다 (idempotent) — PG 는 파일 내용이 아니라 존재
// 자체만 보므로 재생성/덮어쓰기 불필요.
func CreateStandbySignal(dataDir string) error {
	path := filepath.Join(dataDir, standbySignalFile)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	return nil
}
