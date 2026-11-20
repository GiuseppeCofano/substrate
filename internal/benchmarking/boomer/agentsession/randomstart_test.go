// Copyright 2026 The Agent Substrate Authors
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

import "testing"

func TestRandomStartPicksIndexAndFraction(t *testing.T) {
	idx, frac := randomStart(16, func(n int) int { return n - 1 }, func() float64 { return 0.25 })
	if idx != 15 || frac != 0.25 {
		t.Fatalf("randomStart = (%d, %v), want (15, 0.25)", idx, frac)
	}
}

func TestRandomStartEmptyScript(t *testing.T) {
	idx, frac := randomStart(0, func(int) int { t.Fatal("intN called"); return 0 }, func() float64 { return 0.5 })
	if idx != 0 || frac != 1 {
		t.Fatalf("randomStart(0) = (%d, %v), want (0, 1)", idx, frac)
	}
}
