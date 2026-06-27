/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// bufconn.go 는 *읽기 버퍼링* 연결 래퍼다 — per-query 라우팅에서 메시지마다 발생하던
// 헤더(5B)+payload 두 번의 read syscall 을, 연결당 하나의 bufio.Reader 로 모아 줄인다.
// 쓰기는 버퍼링하지 *않고* 그대로 통과시킨다(즉시 전송) — 따라서 flush 시점을 관리할 필요가
// 없고 request/response 교착(deadlock) 위험이 없다. writeMessage 자체가 이미 메시지를 단일
// Write 로 보내므로 쓰기 syscall 도 메시지당 1회다.
package main

import (
	"bufio"
	"net"
)

// bufConn 은 net.Conn 에 읽기 전용 bufio.Reader 를 씌운다. Read 만 버퍼를 거치고, Write/
// Close 등 나머지는 임베드된 net.Conn 으로 직행한다.
type bufConn struct {
	net.Conn
	br *bufio.Reader
}

// newBufConn 은 conn 을 읽기 버퍼(32KiB)로 감싼다. *인증/핸드셰이크가 끝난 뒤* 호출해야
// 한다 — 그래야 bufio.Reader 가 정확히 그 다음 바이트부터 읽는다(중간 바이트 유실 없음).
func newBufConn(conn net.Conn) *bufConn {
	return &bufConn{Conn: conn, br: bufio.NewReaderSize(conn, 32<<10)}
}

// Read 는 버퍼를 통해 읽는다(임베드 net.Conn.Read 를 가린다).
func (b *bufConn) Read(p []byte) (int, error) { return b.br.Read(p) }
