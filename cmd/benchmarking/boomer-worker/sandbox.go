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
	"fmt"
	"time"

	"go.ate.dev/substrate/internal/benchmarking/boomer/sandbox"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// sandboxPollInterval is how often a backend checks whether a new sandbox is
// Ready. It bounds the error on the CreateSandbox row.
const sandboxPollInterval = 250 * time.Millisecond

type sandboxOptions struct {
	backend     string
	namespace   string
	podTemplate string
	warmPool    string
	port        int
}

// newSandbox builds the backend --sandbox-backend names. It returns nil for
// substrate, which runs sessions as actors.
func newSandbox(o sandboxOptions, cfg *rest.Config) (sandbox.Backend, error) {
	switch o.backend {
	case "substrate":
		return nil, nil
	case "pod":
		client, err := kubernetes.NewForConfig(sandboxRESTConfig(cfg))
		if err != nil {
			return nil, err
		}
		return sandbox.NewPod(client, o.namespace, o.podTemplate, o.port, sandboxPollInterval)
	case "warmpool":
		client, err := dynamic.NewForConfig(sandboxRESTConfig(cfg))
		if err != nil {
			return nil, err
		}
		return sandbox.NewWarmPool(client, o.namespace, o.warmPool, o.port, sandboxPollInterval), nil
	}
	return nil, fmt.Errorf("unknown --sandbox-backend %q: want substrate, pod or warmpool", o.backend)
}

// sandboxRESTConfig lifts client-go's default 5 QPS limit. At that limit,
// ready polls for a few dozen new sessions would queue client-side and
// inflate CreateSandbox.
func sandboxRESTConfig(cfg *rest.Config) *rest.Config {
	cfg = rest.CopyConfig(cfg)
	cfg.QPS = 200
	cfg.Burst = 400
	return cfg
}
