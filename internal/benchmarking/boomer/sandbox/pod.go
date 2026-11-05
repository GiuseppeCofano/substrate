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
	"os"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"
)

// Pod creates one Pod per session from a template file.
type Pod struct {
	client       kubernetes.Interface
	namespace    string
	template     *corev1.Pod
	port         int
	pollInterval time.Duration
}

// NewPod reads the Pod template at templatePath. The template must have
// exactly one container, which serves glutton on port.
func NewPod(client kubernetes.Interface, namespace, templatePath string, port int, pollInterval time.Duration) (*Pod, error) {
	data, err := os.ReadFile(templatePath)
	if err != nil {
		return nil, err
	}
	tmpl := &corev1.Pod{}
	if err := yaml.UnmarshalStrict(data, tmpl); err != nil {
		return nil, fmt.Errorf("pod template %s: %w", templatePath, err)
	}
	if n := len(tmpl.Spec.Containers); n != 1 {
		return nil, fmt.Errorf("pod template %s: has %d containers, want 1", templatePath, n)
	}
	return &Pod{client: client, namespace: namespace, template: tmpl, port: port, pollInterval: pollInterval}, nil
}

func (p *Pod) Provision(ctx context.Context, name string) (string, error) {
	pod := p.template.DeepCopy()
	pod.Name = name
	pod.Namespace = p.namespace
	if _, err := p.client.CoreV1().Pods(p.namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		return "", fmt.Errorf("create pod %s: %w", name, err)
	}
	var ip string
	err := wait.PollUntilContextCancel(ctx, p.pollInterval, true, func(ctx context.Context) (bool, error) {
		got, err := p.client.CoreV1().Pods(p.namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if got.Status.Phase == corev1.PodFailed {
			return false, fmt.Errorf("pod %s failed: %s", name, got.Status.Reason)
		}
		if got.Status.PodIP == "" || !podReady(got) {
			return false, nil
		}
		ip = got.Status.PodIP
		return true, nil
	})
	if err != nil {
		return "", errors.Join(fmt.Errorf("pod %s not ready: %w", name, err), cleanup(ctx, p, name))
	}
	return "http://" + net.JoinHostPort(ip, strconv.Itoa(p.port)), nil
}

func (p *Pod) Release(ctx context.Context, name string) error {
	// A benchmark pod has nothing to flush, so it gets no grace period.
	err := p.client.CoreV1().Pods(p.namespace).Delete(ctx, name, metav1.DeleteOptions{GracePeriodSeconds: new(int64)})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func (p *Pod) MemoryLimit(context.Context) (int64, bool, error) {
	q, ok := p.template.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory]
	if !ok {
		return 0, false, nil
	}
	return q.Value(), true, nil
}

func podReady(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}
