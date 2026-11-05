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

// Package sandbox runs a benchmark session's glutton outside Substrate, for
// the plain-Kubernetes baselines: one Pod per session (Pod), or one Agent
// Sandbox claimed from a warm pool (WarmPool).
package sandbox

import (
	"context"
	"time"
)

// SessionLabel marks every Pod and SandboxClaim a backend creates, so
// benchmarking/baselines/deploy.sh can delete the ones a run left behind.
const SessionLabel = "agentsession.ate.dev/session"

// releaseTimeout bounds the cleanup of a sandbox whose provision failed.
const releaseTimeout = 30 * time.Second

// Backend creates and deletes one sandbox per session.
type Backend interface {
	// Provision creates the sandbox called name and returns the base URL
	// of its glutton once the sandbox is Ready. On error it deletes the
	// sandbox again.
	Provision(ctx context.Context, name string) (baseURL string, err error)
	// Release deletes the sandbox called name. A missing sandbox is not an
	// error.
	Release(ctx context.Context, name string) error
	// MemoryLimit reports the sandbox memory limit in bytes. found is false
	// when the template sets none.
	MemoryLimit(ctx context.Context) (limit int64, found bool, err error)
}

// cleanup releases name after a failed provision. ctx may already be done,
// so the release runs on a fresh deadline.
func cleanup(ctx context.Context, b Backend, name string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
	defer cancel()
	return b.Release(ctx, name)
}
