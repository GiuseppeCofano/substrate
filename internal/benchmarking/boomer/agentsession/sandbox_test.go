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

package agentsession

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"go.ate.dev/substrate/internal/benchmarking/boomer/dynconfig"
	"go.ate.dev/substrate/internal/benchmarking/boomer/userclass"
	"go.ate.dev/substrate/internal/benchmarking/glutton/fake"
	"go.opentelemetry.io/otel"
)

type fakeBackend struct {
	url          string
	memory       int64
	provisionErr error

	mu          sync.Mutex
	provisioned []string
	released    []string
}

func (b *fakeBackend) Provision(_ context.Context, name string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.provisioned = append(b.provisioned, name)
	return b.url, b.provisionErr
}

func (b *fakeBackend) Release(_ context.Context, name string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.released = append(b.released, name)
	return nil
}

func (b *fakeBackend) MemoryLimit(context.Context) (int64, bool, error) {
	return b.memory, b.memory != 0, nil
}

// newSandboxRuntime returns a runtime whose sessions run on b against a fake
// glutton. ctl records any control-plane call, which a sandbox session must
// not make.
func newSandboxRuntime(t *testing.T, b *fakeBackend, ctl *fakeControlClient, dyn knobs) *runtime {
	t.Helper()
	ts := (&fake.Server{}).Start(t)
	if b.url == "" {
		b.url = ts.URL
	}
	return &runtime{cfg: &userclass.Config{
		APIStub:    ctl,
		HTTPClient: ts.Client(),
		RouterURL:  "http://router.invalid",
		Atespace:   "benchmark",
		Dyn:        dynconfig.Static(dyn),
		Tracer:     otel.Tracer("test"),
		Sandbox:    b,
	}}
}

var residentKnobs = knobs{Lifecycle: dynconfig.Lifecycle{LifecycleMode: dynconfig.LifecycleModeNone}}

func TestSandboxBackendSession(t *testing.T) {
	b := &fakeBackend{memory: 1 << 30}
	ctl := &fakeControlClient{}
	rt := newSandboxRuntime(t, b, ctl, residentKnobs)
	loaded, err := rt.loadScript()
	if err != nil {
		t.Fatal(err)
	}
	session, err := rt.startUser(context.Background(), 0, loaded)
	if err != nil {
		t.Fatal(err)
	}
	if !session.awake {
		t.Error("sandbox session not awake after start")
	}
	for range 3 {
		if !session.runStep(context.Background(), pingStep) {
			t.Fatal("runStep = false on a sandbox session")
		}
	}
	session.suspendAndDelete(context.Background())
	session.suspendAndDelete(context.Background()) // second call is a no-op

	if calls := ctl.recordedCalls(); len(calls) != 0 {
		t.Errorf("sandbox session made control-plane calls: %v", calls)
	}
	if len(b.provisioned) != 1 || len(b.released) != 1 || b.provisioned[0] != b.released[0] {
		t.Errorf("provisioned %v, released %v; want the same single sandbox", b.provisioned, b.released)
	}
}

func TestSandboxBackendNeedsResidentLifecycle(t *testing.T) {
	b := &fakeBackend{memory: 1 << 30}
	rt := newSandboxRuntime(t, b, &fakeControlClient{}, knobs{
		Lifecycle: dynconfig.Lifecycle{LifecycleMode: dynconfig.LifecycleModeAsync},
	})
	loaded, err := rt.loadScript()
	if err != nil {
		t.Fatal(err)
	}
	_, err = rt.startUser(context.Background(), 0, loaded)
	if err == nil || !strings.Contains(err.Error(), "lifecycle_mode") {
		t.Fatalf("startUser err = %v, want a lifecycle_mode refusal", err)
	}
	if len(b.provisioned) != 0 {
		t.Errorf("refused start still provisioned %v", b.provisioned)
	}
}

func TestSandboxProvisionFailure(t *testing.T) {
	b := &fakeBackend{memory: 1 << 30, url: "http://unused.invalid", provisionErr: errors.New("pool empty")}
	rt := newSandboxRuntime(t, b, &fakeControlClient{}, residentKnobs)
	loaded, err := rt.loadScript()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.startUser(context.Background(), 0, loaded); err == nil || !strings.Contains(err.Error(), "pool empty") {
		t.Fatalf("startUser err = %v, want the provision error", err)
	}
}

func TestStartUserChecksSandboxMemory(t *testing.T) {
	b := &fakeBackend{memory: 256 << 20}
	rt := newSandboxRuntime(t, b, &fakeControlClient{}, residentKnobs)
	loaded, err := rt.loadScript()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.startUser(context.Background(), 0, loaded); err == nil || !strings.Contains(err.Error(), "min_actor_memory") {
		t.Fatalf("startUser err = %v, want a memory refusal", err)
	}
	if len(b.provisioned) != 0 {
		t.Errorf("refused start still provisioned %v", b.provisioned)
	}
}
