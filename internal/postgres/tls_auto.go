/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package postgres

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"time"
)

// D.6.1 Built-in TLS auto-issuance (ROADMAP G2 L126).
//
// cert-manager 없는 환경 (e.g. minikube / kind / air-gapped) 에서 operator 가
// in-process 로 RSA-2048 + x509 self-signed 인증서를 생성하여 Pooler /
// postgres 자체 TLS endpoint 를 부트스트랩한다.
//
// 본 패키지는 *순수 crypto 함수* — K8s Secret 작성은 별 layer.

// ErrInvalidTLSSpec 는 TLSAutoSpec validation 실패 시 반환.
var ErrInvalidTLSSpec = errors.New("postgres: invalid TLSAutoSpec")

// TLSAutoSpec 는 self-signed 인증서 발급 사용자 의도이다.
type TLSAutoSpec struct {
	// CommonName 은 인증서 CN (보통 Pooler / PostgresCluster 의 Service FQDN).
	CommonName string
	// SANs 는 Subject Alternative Names — DNS 이름 + IP 의 union.
	SANs []string
	// Organization 은 cert subject 의 O. 기본 "postgres-operator".
	Organization string
	// ValidFor 는 인증서 유효 기간. 0 이면 365 일.
	ValidFor time.Duration
	// KeyBits 는 RSA 키 크기. 0 이면 2048. 1024 미만 거부.
	KeyBits int
	// NotBefore 는 인증서 시작 시각. zero 면 time.Now().
	NotBefore time.Time
}

// TLSBundle 는 발급 결과 PEM-encoded cert + key + CA (self-signed 의 경우 cert==ca).
type TLSBundle struct {
	// CertPEM 는 PEM-encoded leaf cert.
	CertPEM []byte
	// KeyPEM 는 PEM-encoded RSA private key (PKCS#1).
	KeyPEM []byte
	// CAPEM 는 PEM-encoded CA cert. self-signed 의 경우 CertPEM 동일.
	CAPEM []byte
	// NotBefore / NotAfter 는 상태 surface (`Pooler.Status.AutoTLS*`).
	NotBefore time.Time
	NotAfter  time.Time
}

// IssueSelfSigned 는 spec 으로부터 self-signed RSA-2048 cert + key 를 발급한다.
//
// 발급 path:
//  1. validate spec (CN 필수, 1+ SAN, valid duration)
//  2. RSA-N keypair 생성
//  3. x509 template — 365d default, CA + leaf 통합 (self-signed),
//     ExtKeyUsage=ServerAuth+ClientAuth, KeyUsage=DigitalSignature+KeyEncipherment+CertSign
//  4. x509.CreateCertificate (self-signed: parent==template)
//  5. PEM encode → TLSBundle 반환
//
// 결정성: 동일 spec 도 매번 다른 결과 — RSA 키는 본질적으로 random. 테스트는
// *구조* (PEM valid + spec 일치) 만 검증.
func IssueSelfSigned(spec TLSAutoSpec) (*TLSBundle, error) {
	if err := validateTLSSpec(spec); err != nil {
		return nil, err
	}

	keyBits := spec.KeyBits
	if keyBits == 0 {
		keyBits = 2048
	}
	validFor := spec.ValidFor
	if validFor == 0 {
		validFor = 365 * 24 * time.Hour
	}
	notBefore := spec.NotBefore
	if notBefore.IsZero() {
		notBefore = time.Now()
	}
	notAfter := notBefore.Add(validFor)

	priv, err := rsa.GenerateKey(rand.Reader, keyBits)
	if err != nil {
		return nil, fmt.Errorf("postgres: rsa keygen: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("postgres: serial: %w", err)
	}

	org := spec.Organization
	if org == "" {
		org = "postgres-operator"
	}

	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   spec.CommonName,
			Organization: []string{org},
		},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              spec.SANs,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return nil, fmt.Errorf("postgres: CreateCertificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(priv),
	})

	return &TLSBundle{
		CertPEM:   certPEM,
		KeyPEM:    keyPEM,
		CAPEM:     certPEM, // self-signed 의 경우 동일.
		NotBefore: notBefore,
		NotAfter:  notAfter,
	}, nil
}

// ShouldRenew 는 인증서가 갱신 임계점에 도달했는지 검사한다.
//
// 정책: notAfter - now < skew → true (default skew = 30 일).
// HANDOFF T29 의 "30-day renewal skew" 일관 적용.
func ShouldRenew(bundle *TLSBundle, now time.Time, skew time.Duration) bool {
	if bundle == nil {
		return true
	}
	if skew == 0 {
		skew = 30 * 24 * time.Hour
	}
	return bundle.NotAfter.Sub(now) < skew
}

func validateTLSSpec(spec TLSAutoSpec) error {
	if spec.CommonName == "" {
		return fmt.Errorf("%w: empty CommonName", ErrInvalidTLSSpec)
	}
	if len(spec.SANs) == 0 {
		return fmt.Errorf("%w: at least 1 SAN required", ErrInvalidTLSSpec)
	}
	if spec.KeyBits > 0 && spec.KeyBits < 2048 {
		return fmt.Errorf("%w: KeyBits=%d < 2048 (security floor)", ErrInvalidTLSSpec, spec.KeyBits)
	}
	if spec.ValidFor < 0 {
		return fmt.Errorf("%w: ValidFor=%s must be non-negative", ErrInvalidTLSSpec, spec.ValidFor)
	}
	return nil
}
