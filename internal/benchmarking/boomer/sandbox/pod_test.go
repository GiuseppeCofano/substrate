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
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

const testPodTemplate = `
apiVersion: v1
kind: Pod
metadata:
  labels:
    app: agentsession-sandbox
spec:
  restartPolicy: Never
  containers:
  - name: glutton
    image: example.com/glutton:test
    resources:
      limits:
        memory: 1Gi
`

func writeTemplate(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pod.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// readyOnCreate marks every created Pod Ready with ip, as the kubelet would.
func readyOnCreate(client *fake.Clientset, ip string) {
	client.PrependReactor("create", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		pod := a.(k8stesting.CreateAction).GetObject().(*corev1.Pod)
		pod.Status.PodIP = ip
		pod.Status.Phase = corev1.PodRunning
		pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
		return false, nil, nil
	})
}

func TestPodProvision(t *testing.T) {
	client := fake.NewClientset()
	readyOnCreate(client, "10.0.0.7")
	p, err := NewPod(client, "sb", writeTemplate(t, testPodTemplate), 8080, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	url, err := p.Provision(context.Background(), "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	if url != "http://10.0.0.7:8080" {
		t.Errorf("Provision = %q, want http://10.0.0.7:8080", url)
	}
	got, err := client.CoreV1().Pods("sb").Get(context.Background(), "agent-1", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Labels["app"] != "agentsession-sandbox" || got.Labels[SessionLabel] != "true" {
		t.Errorf("pod labels = %v, want the template's plus %s", got.Labels, SessionLabel)
	}
}

func TestPodProvisionTimesOutAndCleansUp(t *testing.T) {
	client := fake.NewClientset() // no reactor: the Pod stays Pending
	p, err := NewPod(client, "sb", writeTemplate(t, testPodTemplate), 8080, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := p.Provision(ctx, "agent-1"); err == nil {
		t.Fatal("Provision of a Pending pod succeeded, want error")
	}
	_, err = client.CoreV1().Pods("sb").Get(context.Background(), "agent-1", metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		t.Errorf("Pending pod left behind: Get err = %v, want NotFound", err)
	}
}

func TestPodProvisionFailsOnFailedPod(t *testing.T) {
	client := fake.NewClientset()
	client.PrependReactor("create", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		pod := a.(k8stesting.CreateAction).GetObject().(*corev1.Pod)
		pod.Status.Phase = corev1.PodFailed
		pod.Status.Reason = "OOMKilled"
		return false, nil, nil
	})
	p, err := NewPod(client, "sb", writeTemplate(t, testPodTemplate), 8080, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Provision(context.Background(), "agent-1"); err == nil {
		t.Fatal("Provision of a Failed pod succeeded, want error")
	}
}

func TestPodReleaseIgnoresNotFound(t *testing.T) {
	p, err := NewPod(fake.NewClientset(), "sb", writeTemplate(t, testPodTemplate), 8080, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Release(context.Background(), "never-created"); err != nil {
		t.Errorf("Release of a missing pod = %v, want nil", err)
	}
}

func TestPodMemoryLimit(t *testing.T) {
	p, err := NewPod(fake.NewClientset(), "sb", writeTemplate(t, testPodTemplate), 8080, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	limit, found, err := p.MemoryLimit(context.Background())
	if err != nil || !found || limit != 1<<30 {
		t.Errorf("MemoryLimit = %d, %v, %v; want 1Gi, true, nil", limit, found, err)
	}
}

func TestNewPodRejectsBadTemplate(t *testing.T) {
	for name, body := range map[string]string{
		"two containers": testPodTemplate + "  - name: extra\n    image: x\n",
		"unknown field":  testPodTemplate + "  bogus: true\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewPod(fake.NewClientset(), "sb", writeTemplate(t, body), 8080, time.Millisecond); err == nil {
				t.Error("NewPod accepted the template, want error")
			}
		})
	}
}
