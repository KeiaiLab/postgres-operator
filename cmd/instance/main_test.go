/*
Copyright 2026 keiailab.

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

package main

import "testing"

func TestBuildElectionIdentity_UsesPodUID(t *testing.T) {
	got := buildElectionIdentity("demo-shard-0-0", "uid-123")
	if got != "demo-shard-0-0/uid-123" {
		t.Fatalf("identity = %q, want podName/podUID", got)
	}
}

func TestParsePodOrdinalOrDie_StatefulSetName(t *testing.T) {
	got := parsePodOrdinalOrDie("demo-shard-0-12")
	if got != 12 {
		t.Fatalf("ordinal = %d, want 12", got)
	}
}

func TestStandbyCandidateEndpoint_OrdinalOne(t *testing.T) {
	got := standbyCandidateEndpoint("demo", 0, "ns1")
	want := "demo-shard-0-1.demo-shard-0-headless.ns1.svc.cluster.local:5432"
	if got != want {
		t.Fatalf("endpoint = %q, want %q", got, want)
	}
}
