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
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
)

// The Agent Sandbox extension resources (sigs.k8s.io/agent-sandbox). The
// dynamic client reaches them without a module dependency.
var (
	claimGVR    = schema.GroupVersionResource{Group: "extensions.agents.x-k8s.io", Version: "v1beta1", Resource: "sandboxclaims"}
	poolGVR     = schema.GroupVersionResource{Group: "extensions.agents.x-k8s.io", Version: "v1beta1", Resource: "sandboxwarmpools"}
	templateGVR = schema.GroupVersionResource{Group: "extensions.agents.x-k8s.io", Version: "v1beta1", Resource: "sandboxtemplates"}
)

// WarmPool claims one sandbox per session from a SandboxWarmPool.
type WarmPool struct {
	client       dynamic.Interface
	namespace    string
	pool         string
	port         int
	pollInterval time.Duration
}

// NewWarmPool returns a backend that claims from the SandboxWarmPool called
// pool. Each sandbox serves glutton on port.
func NewWarmPool(client dynamic.Interface, namespace, pool string, port int, pollInterval time.Duration) *WarmPool {
	return &WarmPool{client: client, namespace: namespace, pool: pool, port: port, pollInterval: pollInterval}
}

func (w *WarmPool) Provision(ctx context.Context, name string) (string, error) {
	claim := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "extensions.agents.x-k8s.io/v1beta1",
		"kind":       "SandboxClaim",
		"metadata":   map[string]any{"name": name, "namespace": w.namespace},
		"spec":       map[string]any{"warmPoolRef": map[string]any{"name": w.pool}},
	}}
	claims := w.client.Resource(claimGVR).Namespace(w.namespace)
	if _, err := claims.Create(ctx, claim, metav1.CreateOptions{}); err != nil {
		return "", fmt.Errorf("create sandboxclaim %s: %w", name, err)
	}
	var ip string
	err := wait.PollUntilContextCancel(ctx, w.pollInterval, true, func(ctx context.Context) (bool, error) {
		got, err := claims.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		ips, _, _ := unstructured.NestedStringSlice(got.Object, "status", "sandbox", "podIPs")
		if len(ips) == 0 || !claimReady(got) {
			return false, nil
		}
		ip = ips[0]
		return true, nil
	})
	if err != nil {
		return "", errors.Join(fmt.Errorf("sandboxclaim %s not ready: %w", name, err), cleanup(ctx, w, name))
	}
	return "http://" + net.JoinHostPort(ip, strconv.Itoa(w.port)), nil
}

func (w *WarmPool) Release(ctx context.Context, name string) error {
	err := w.client.Resource(claimGVR).Namespace(w.namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// MemoryLimit reads the container memory limit from the template the warm
// pool references.
func (w *WarmPool) MemoryLimit(ctx context.Context) (int64, bool, error) {
	pool, err := w.client.Resource(poolGVR).Namespace(w.namespace).Get(ctx, w.pool, metav1.GetOptions{})
	if err != nil {
		return 0, false, fmt.Errorf("get sandboxwarmpool %s: %w", w.pool, err)
	}
	tmplName, _, _ := unstructured.NestedString(pool.Object, "spec", "sandboxTemplateRef", "name")
	tmpl, err := w.client.Resource(templateGVR).Namespace(w.namespace).Get(ctx, tmplName, metav1.GetOptions{})
	if err != nil {
		return 0, false, fmt.Errorf("get sandboxtemplate %q: %w", tmplName, err)
	}
	containers, _, _ := unstructured.NestedSlice(tmpl.Object, "spec", "podTemplate", "spec", "containers")
	if len(containers) != 1 {
		return 0, false, fmt.Errorf("sandboxtemplate %s: has %d containers, want 1", tmplName, len(containers))
	}
	c, _ := containers[0].(map[string]any)
	raw, found, _ := unstructured.NestedString(c, "resources", "limits", "memory")
	if !found {
		return 0, false, nil
	}
	q, err := resource.ParseQuantity(raw)
	if err != nil {
		return 0, false, fmt.Errorf("sandboxtemplate %s memory limit %q: %w", tmplName, raw, err)
	}
	return q.Value(), true, nil
}

func claimReady(claim *unstructured.Unstructured) bool {
	conds, _, _ := unstructured.NestedSlice(claim.Object, "status", "conditions")
	for _, c := range conds {
		m, _ := c.(map[string]any)
		if m["type"] == "Ready" {
			return m["status"] == "True"
		}
	}
	return false
}
