/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// scram.go 는 라우터가 *백엔드 인증을 대행* 하는 로직이다 — query-mode 가 trust 가 아닌
// 실 PostgreSQL(scram-sha-256 / cleartext)과 동작하도록. 백엔드가 startup 직후 인증을
// 요구하면, 라우터가 설정된 비밀번호(PGROUTER_BACKEND_PASSWORD)로 핸드셰이크를 완료한 뒤
// ReadyForQuery 까지 소비한다. (클라이언트 인증은 trust — pgbouncer 처럼 라우터가 백엔드
// 자격을 보유하는 모델. 클라이언트측 인증 강제는 후속.)
//
// SCRAM-SHA-256 은 Go 1.24+ stdlib(crypto/pbkdf2) + crypto/hmac/sha256 으로 구현 —
// 외부 의존성 없음. RFC 5802 / PostgreSQL SASL.
package main

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// authenticateAndDrain 은 백엔드의 startup 응답을 처리한다: 인증 요구(SASL/cleartext)면
// 대행하고, AuthenticationOk·이후 ParameterStatus/BackendKeyData 를 지나 ReadyForQuery
// ('Z')까지 소비한다. 클라이언트는 이미 라우터의 trust 핸드셰이크를 받았으므로 이 메시지들을
// 흘려보내지 않는다.
func authenticateAndDrain(server net.Conn, password string) error {
	for {
		m, err := readMessage(server)
		if err != nil {
			return err
		}
		switch m.Type {
		case 'Z': // ReadyForQuery
			return nil
		case 'E': // ErrorResponse
			return fmt.Errorf("backend error: %s", string(m.Payload))
		case 'R': // AuthenticationRequest
			switch be32(m.Payload) {
			case 0: // AuthenticationOk
			case 3: // Cleartext password
				if err := writeMessage(server, 'p', cstring(password)); err != nil {
					return err
				}
			case 10: // SASL (SCRAM-SHA-256)
				if err := scramAuth(server, m, password); err != nil {
					return err
				}
			default:
				return fmt.Errorf("unsupported backend auth method %d (need trust/cleartext/scram-sha-256)", be32(m.Payload))
			}
		}
	}
}

// scramAuth 는 AuthenticationSASL 메시지를 받은 직후부터 SCRAM-SHA-256 클라이언트
// 핸드셰이크를 수행한다.
func scramAuth(server net.Conn, sasl pgMessage, password string) error {
	if len(sasl.Payload) < 4 || !strings.Contains(string(sasl.Payload[4:]), "SCRAM-SHA-256") {
		return fmt.Errorf("backend SASL does not offer SCRAM-SHA-256")
	}
	clientNonce, err := genNonce()
	if err != nil {
		return err
	}
	clientFirstBare := "n=,r=" + clientNonce
	clientFirst := "n,," + clientFirstBare

	// SASLInitialResponse: mechanism(cstring) + Int32 len + client-first.
	init := cstring("SCRAM-SHA-256")
	init = appendInt32(init, int32(len(clientFirst)))
	init = append(init, clientFirst...)
	if err := writeMessage(server, 'p', init); err != nil {
		return err
	}

	// AuthenticationSASLContinue (R, 11) + server-first.
	m, err := readMessage(server)
	if err != nil {
		return err
	}
	if m.Type != 'R' || be32(m.Payload) != 11 {
		return fmt.Errorf("expected SASLContinue, got %c/%d", m.Type, be32(m.Payload))
	}
	serverFirst := string(m.Payload[4:])
	attrs := parseScramAttrs(serverFirst)
	combined := attrs["r"]
	if !strings.HasPrefix(combined, clientNonce) {
		return fmt.Errorf("scram: server nonce mismatch")
	}
	salt, err := base64.StdEncoding.DecodeString(attrs["s"])
	if err != nil {
		return fmt.Errorf("scram: bad salt: %w", err)
	}
	iter, err := strconv.Atoi(attrs["i"])
	if err != nil {
		return fmt.Errorf("scram: bad iteration count: %w", err)
	}

	clientFinalBare := "c=biws,r=" + combined
	authMsg := clientFirstBare + "," + serverFirst + "," + clientFinalBare
	proofB64, err := scramClientProof(password, salt, iter, authMsg)
	if err != nil {
		return fmt.Errorf("scram: %w", err)
	}
	clientFinal := clientFinalBare + ",p=" + proofB64

	// SASLResponse.
	if err := writeMessage(server, 'p', []byte(clientFinal)); err != nil {
		return err
	}

	// AuthenticationSASLFinal (R, 12) then AuthenticationOk (R, 0).
	m, err = readMessage(server)
	if err != nil {
		return err
	}
	if m.Type == 'R' && be32(m.Payload) == 12 { // server-final (v=...); 서명 검증은 생략.
		m, err = readMessage(server)
		if err != nil {
			return err
		}
	}
	if m.Type == 'E' {
		return fmt.Errorf("backend auth failed: %s", string(m.Payload))
	}
	if m.Type == 'R' && be32(m.Payload) == 0 {
		return nil // AuthenticationOk
	}
	return fmt.Errorf("scram: unexpected message after handshake: %c", m.Type)
}

// scramClientProof 는 SCRAM-SHA-256 의 ClientProof(base64)를 계산한다.
//
//	SaltedPassword = PBKDF2(password, salt, i)
//	ClientKey      = HMAC(SaltedPassword, "Client Key")
//	StoredKey      = SHA256(ClientKey)
//	ClientSig      = HMAC(StoredKey, AuthMessage)
//	ClientProof    = ClientKey XOR ClientSig
func scramClientProof(password string, salt []byte, iter int, authMessage string) (string, error) {
	salted, err := pbkdf2.Key(sha256.New, password, salt, iter, 32)
	if err != nil {
		return "", fmt.Errorf("pbkdf2: %w", err)
	}
	clientKey := hmacSum(salted, []byte("Client Key"))
	storedKey := sha256.Sum256(clientKey)
	clientSig := hmacSum(storedKey[:], []byte(authMessage))
	return base64.StdEncoding.EncodeToString(xorBytes(clientKey, clientSig)), nil
}

func genNonce() (string, error) {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// parseScramAttrs 는 "k=v,k=v" 형식을 맵으로. 값에 '='(base64 패딩)가 있어도 첫 '='만
// 분리자로 본다(키는 단일 문자).
func parseScramAttrs(s string) map[string]string {
	m := map[string]string{}
	for _, part := range strings.Split(s, ",") {
		if i := strings.IndexByte(part, '='); i > 0 {
			m[part[:i]] = part[i+1:]
		}
	}
	return m
}

func hmacSum(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

func xorBytes(a, b []byte) []byte {
	out := make([]byte, len(a))
	for i := range a {
		out[i] = a[i] ^ b[i]
	}
	return out
}

func be32(p []byte) uint32 {
	if len(p) < 4 {
		return 0
	}
	return binary.BigEndian.Uint32(p)
}

func appendInt32(p []byte, v int32) []byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(v))
	return append(p, b[:]...)
}
