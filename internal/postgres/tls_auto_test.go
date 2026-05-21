/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package postgres

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"testing"
	"time"
)

func TestIssueSelfSigned(t *testing.T) {
	t.Run("default 발급 + PEM parse", func(t *testing.T) {
		spec := TLSAutoSpec{
			CommonName: "pooler.cluster.svc",
			SANs:       []string{"pooler.cluster.svc", "pooler.cluster.svc.cluster.local"},
		}
		b, err := IssueSelfSigned(spec)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		// PEM block parse
		block, _ := pem.Decode(b.CertPEM)
		if block == nil || block.Type != "CERTIFICATE" {
			t.Fatalf("cert PEM decode failed: %+v", block)
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatalf("cert parse: %v", err)
		}
		if cert.Subject.CommonName != spec.CommonName {
			t.Fatalf("CN want=%s got=%s", spec.CommonName, cert.Subject.CommonName)
		}
		if len(cert.DNSNames) != 2 {
			t.Fatalf("DNSNames want=2 got=%d", len(cert.DNSNames))
		}
		if !cert.IsCA {
			t.Fatalf("self-signed cert must be CA")
		}
		// 키 사용
		hasServer, hasClient := false, false
		for _, eku := range cert.ExtKeyUsage {
			if eku == x509.ExtKeyUsageServerAuth {
				hasServer = true
			}
			if eku == x509.ExtKeyUsageClientAuth {
				hasClient = true
			}
		}
		if !hasServer || !hasClient {
			t.Fatalf("ExtKeyUsage must include ServerAuth+ClientAuth, got %+v", cert.ExtKeyUsage)
		}
	})

	t.Run("ValidFor + NotBefore 결정성", func(t *testing.T) {
		nb := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		dur := 90 * 24 * time.Hour
		b, err := IssueSelfSigned(TLSAutoSpec{
			CommonName: "x.svc", SANs: []string{"x.svc"},
			NotBefore: nb, ValidFor: dur,
		})
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if !b.NotBefore.Equal(nb) {
			t.Fatalf("NotBefore want=%v got=%v", nb, b.NotBefore)
		}
		if !b.NotAfter.Equal(nb.Add(dur)) {
			t.Fatalf("NotAfter want=%v got=%v", nb.Add(dur), b.NotAfter)
		}
	})

	t.Run("key PEM 도 valid", func(t *testing.T) {
		b, _ := IssueSelfSigned(TLSAutoSpec{CommonName: "x.svc", SANs: []string{"x.svc"}})
		block, _ := pem.Decode(b.KeyPEM)
		if block == nil || block.Type != "RSA PRIVATE KEY" {
			t.Fatalf("key PEM decode: %+v", block)
		}
		if _, err := x509.ParsePKCS1PrivateKey(block.Bytes); err != nil {
			t.Fatalf("key parse: %v", err)
		}
	})

	t.Run("self-signed CAPEM == CertPEM", func(t *testing.T) {
		b, _ := IssueSelfSigned(TLSAutoSpec{CommonName: "x.svc", SANs: []string{"x.svc"}})
		if string(b.CAPEM) != string(b.CertPEM) {
			t.Fatalf("self-signed bundle CAPEM must equal CertPEM")
		}
	})

	t.Run("validation 거부", func(t *testing.T) {
		cases := []TLSAutoSpec{
			{SANs: []string{"x"}}, // empty CN
			{CommonName: "cn"},    // empty SAN
			{CommonName: "cn", SANs: []string{"x"}, KeyBits: 512}, // too small
			{CommonName: "cn", SANs: []string{"x"}, ValidFor: -1}, // negative
		}
		for i, c := range cases {
			_, err := IssueSelfSigned(c)
			if !errors.Is(err, ErrInvalidTLSSpec) {
				t.Fatalf("case[%d]: want ErrInvalidTLSSpec, got %v", i, err)
			}
		}
	})

	t.Run("Organization custom", func(t *testing.T) {
		b, _ := IssueSelfSigned(TLSAutoSpec{
			CommonName: "x.svc", SANs: []string{"x.svc"},
			Organization: "custom-org",
		})
		block, _ := pem.Decode(b.CertPEM)
		cert, _ := x509.ParseCertificate(block.Bytes)
		if len(cert.Subject.Organization) != 1 || cert.Subject.Organization[0] != "custom-org" {
			t.Fatalf("Organization want=[custom-org] got=%+v", cert.Subject.Organization)
		}
	})
}

func TestShouldRenew(t *testing.T) {
	now := time.Date(2026, 5, 19, 0, 0, 0, 0, time.UTC)

	t.Run("nil bundle 는 renew 필요", func(t *testing.T) {
		if !ShouldRenew(nil, now, 0) {
			t.Fatal("nil bundle must require renew")
		}
	})

	t.Run("notAfter 30일 미만이면 renew", func(t *testing.T) {
		b := &TLSBundle{NotAfter: now.Add(20 * 24 * time.Hour)}
		if !ShouldRenew(b, now, 0) {
			t.Fatal("20일 < 30일 default skew → renew")
		}
	})

	t.Run("notAfter 60일 남으면 renew 불필요", func(t *testing.T) {
		b := &TLSBundle{NotAfter: now.Add(60 * 24 * time.Hour)}
		if ShouldRenew(b, now, 0) {
			t.Fatal("60일 > 30일 skew → no renew")
		}
	})

	t.Run("custom skew", func(t *testing.T) {
		b := &TLSBundle{NotAfter: now.Add(40 * 24 * time.Hour)}
		if !ShouldRenew(b, now, 60*24*time.Hour) {
			t.Fatal("40일 < 60일 skew → renew")
		}
	})
}
