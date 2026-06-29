/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package main

import (
	"encoding/base64"
	"testing"
)

// TestScramClientProof 는 SCRAM-SHA-256 계산을 RFC 7677 §3 테스트 벡터로 검증한다.
func TestScramClientProof(t *testing.T) {
	password := "pencil"
	salt, _ := base64.StdEncoding.DecodeString("W22ZaJ0SNY7soEsUEjb6gQ==")
	iter := 4096
	clientFirstBare := "n=user,r=rOprNGfwEbeRWgbNEkqO"
	serverFirst := "r=rOprNGfwEbeRWgbNEkqO%hvYDpWUa2RaTCAfuxFIlj)hNlF$k0,s=W22ZaJ0SNY7soEsUEjb6gQ==,i=4096"
	clientFinalBare := "c=biws,r=rOprNGfwEbeRWgbNEkqO%hvYDpWUa2RaTCAfuxFIlj)hNlF$k0"
	authMsg := clientFirstBare + "," + serverFirst + "," + clientFinalBare

	proof, err := scramClientProof(password, salt, iter, authMsg)
	if err != nil {
		t.Fatalf("scramClientProof: %v", err)
	}
	want := "dHzbZapWIk4jUhN+Ute9ytag9zjfMHgsqmmiz7AndVQ="
	if proof != want {
		t.Fatalf("ClientProof = %q, want %q (RFC 7677)", proof, want)
	}
}

// TestParseScramAttrs 는 server-first 속성 파싱(값에 base64 '=' 포함)을 검증.
func TestParseScramAttrs(t *testing.T) {
	a := parseScramAttrs("r=abc%def,s=W22ZaJ0SNY7soEsUEjb6gQ==,i=4096")
	if a["r"] != "abc%def" || a["s"] != "W22ZaJ0SNY7soEsUEjb6gQ==" || a["i"] != "4096" {
		t.Fatalf("parseScramAttrs = %v", a)
	}
}
