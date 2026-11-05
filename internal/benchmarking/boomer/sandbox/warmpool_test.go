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

package sandbox

import (
	"context"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

func agentSandboxObject(kind, name string, spec map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "extensions.agents.x-k8s.io/v1beta1",
		"kind":       kind,
		"metadata":   map[string]any{"name": name, "namespace": "sb"},
		"spec":       spec,
	}}
}

func newFakeDynamic(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	lists := map[schema.GroupVersionResource]string{
		claimGVR:    "SandboxClaimList",
		poolGVR:     "SandboxWarmPoolList",
		templateGVR: "SandboxTemplateList",
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), lists, objs...)
}

// bindOnCreate marks every created claim Ready and bound to a sandbox at ip,
// as the Agent Sandbox controller would.
func bindOnCreate(client *dynamicfake.FakeDynamicClient, ip string) {
	client.PrependReactor("create", "sandboxclaims", func(a k8stesting.Action) (bool, runtime.Object, error) {
		claim := a.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured)
		_ = unstructured.SetNestedField(claim.Object, map[string]any{
			"conditions": []any{map[string]any{"type": "Ready", "status": "True"}},
			"sandbox":    map[string]any{"name": claim.GetName(), "podIPs": []any{ip}},
		}, "status")
		return false, nil, nil
	})
}

func TestWarmPoolProvision(t *testing.T) {
	client := newFakeDynamic()
	bindOnCreate(client, "10.0.0.9")
	w := NewWarmPool(client, "sb", "glutton-pool", 8080, time.Millisecond)
	url, err := w.Provision(context.Background(), "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	if url != "http://10.0.0.9:8080" {
		t.Errorf("Provision = %q, want http://10.0.0.9:8080", url)
	}
	claim, err := client.Resource(claimGVR).Namespace("sb").Get(context.Background(), "agent-1", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if pool, _, _ := unstructured.NestedString(claim.Object, "spec", "warmPoolRef", "name"); pool != "glutton-pool" {
		t.Errorf("claim warmPoolRef = %q, want glutton-pool", pool)
	}
}

func TestWarmPoolProvisionTimesOutAndCleansUp(t *testing.T) {
	client := newFakeDynamic() // no reactor: the claim never binds
	w := NewWarmPool(client, "sb", "glutton-pool", 8080, time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := w.Provision(ctx, "agent-1"); err == nil {
		t.Fatal("Provision of an unbound claim succeeded, want error")
	}
	_, err := client.Resource(claimGVR).Namespace("sb").Get(context.Background(), "agent-1", metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		t.Errorf("unbound claim left behind: Get err = %v, want NotFound", err)
	}
}

func TestWarmPoolReleaseIgnoresNotFound(t *testing.T) {
	w := NewWarmPool(newFakeDynamic(), "sb", "glutton-pool", 8080, time.Millisecond)
	if err := w.Release(context.Background(), "never-created"); err != nil {
		t.Errorf("Release of a missing claim = %v, want nil", err)
	}
}

func TestWarmPoolMemoryLimit(t *testing.T) {
	pool := agentSandboxObject("SandboxWarmPool", "glutton-pool", map[string]any{
		"replicas":           int64(5),
		"sandboxTemplateRef": map[string]any{"name": "glutton"},
	})
	tmpl := agentSandboxObject("SandboxTemplate", "glutton", map[string]any{
		"podTemplate": map[string]any{"spec": map[string]any{"containers": []any{
			map[string]any{"name": "glutton", "resources": map[string]any{"limits": map[string]any{"memory": "1Gi"}}},
		}}},
	})
	w := NewWarmPool(newFakeDynamic(pool, tmpl), "sb", "glutton-pool", 8080, time.Millisecond)
	limit, found, err := w.MemoryLimit(context.Background())
	if err != nil || !found || limit != 1<<30 {
		t.Errorf("MemoryLimit = %d, %v, %v; want 1Gi, true, nil", limit, found, err)
	}
}
