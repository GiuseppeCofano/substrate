// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"os"
	"path/filepath"
	"testing"

	"k8s.io/client-go/rest"
)

func TestSandboxRESTConfigRaisesLimits(t *testing.T) {
	got := sandboxRESTConfig(&rest.Config{Host: "https://example.invalid"})
	if got.QPS < 100 || got.Burst < 200 {
		t.Errorf("QPS, Burst = %v, %d; want at least 100, 200", got.QPS, got.Burst)
	}
}

func TestNewSandbox(t *testing.T) {
	tmpl := filepath.Join(t.TempDir(), "pod.yaml")
	if err := os.WriteFile(tmpl, []byte("spec:\n  containers:\n  - name: glutton\n    image: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &rest.Config{Host: "https://example.invalid"}
	for _, tc := range []struct {
		backend string
		wantNil bool
		wantErr bool
	}{
		{backend: "substrate", wantNil: true},
		{backend: "pod"},
		{backend: "warmpool"},
		{backend: "bogus", wantErr: true},
	} {
		t.Run(tc.backend, func(t *testing.T) {
			b, err := newSandbox(sandboxOptions{backend: tc.backend, namespace: "sb", podTemplate: tmpl, warmPool: "glutton-pool", port: 8080}, cfg)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && (b == nil) != tc.wantNil {
				t.Errorf("backend = %v, wantNil %v", b, tc.wantNil)
			}
		})
	}
}
