/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package router

import "testing"

// TestParseHashBound_Bounds 는 uint32 를 넘는 bound 를 잘라 엉뚱한 범위로
// 해석하지 않고 에러를 내는지 검증한다 (예: 0x100000000 → 0 이 되면 안 된다).
func TestParseHashBound_Bounds(t *testing.T) {
	tests := []struct {
		in      string
		want    uint32
		wantErr bool
	}{
		{in: "0", want: 0},
		{in: "0x00000000", want: 0},
		{in: "0xffffffff", want: 0xffffffff},
		{in: "ffffffff", want: 0xffffffff},
		{in: "4294967295", want: 0xffffffff},
		{in: "4294967296", wantErr: true},
		{in: "0x100000000", wantErr: true},
		{in: "100000000f", wantErr: true},
		{in: "-1", wantErr: true},
		{in: "zz", wantErr: true},
	}
	for _, tt := range tests {
		got, err := parseHashBound(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("%q: want error, got %#x", tt.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: unexpected error: %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("%q: got %#x, want %#x", tt.in, got, tt.want)
		}
	}
}
