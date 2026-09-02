/**
 * Copyright 2018 Advanced Micro Devices, Inc.  All rights reserved.
 *
 *  Licensed under the Apache License, Version 2.0 (the "License");
 *  you may not use this file except in compliance with the License.
 *  You may obtain a copy of the License at
 *
 *      http://www.apache.org/licenses/LICENSE-2.0
 *
 *  Unless required by applicable law or agreed to in writing, software
 *  distributed under the License is distributed on an "AS IS" BASIS,
 *  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 *  See the License for the specific language governing permissions and
 *  limitations under the License.
**/

package main

import (
	"os"
	"testing"
)

func TestNodeNameFromEnv(t *testing.T) {
	t.Setenv(nodeNameEnvVar, "")
	t.Setenv("DS_NODE_NAME", "")

	if got := nodeNameFromEnv(); got != "" {
		t.Fatalf("nodeNameFromEnv() = %q, want empty", got)
	}

	t.Setenv(nodeNameEnvVar, "node-a")
	if got := nodeNameFromEnv(); got != "node-a" {
		t.Fatalf("nodeNameFromEnv() = %q, want node-a", got)
	}

	t.Setenv(nodeNameEnvVar, "")
	t.Setenv("DS_NODE_NAME", "node-b")
	if got := nodeNameFromEnv(); got != "node-b" {
		t.Fatalf("nodeNameFromEnv() = %q, want node-b", got)
	}

	t.Setenv(nodeNameEnvVar, "node-a")
	if got := nodeNameFromEnv(); got != "node-a" {
		t.Fatalf("nodeNameFromEnv() = %q, want node-a when both are set", got)
	}
	_ = os.Unsetenv(nodeNameEnvVar)
}

func TestGPUKindForName(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{name: "AMD Radeon RX 7900 XTX", want: gpuKindDiscrete},
		{name: "AMD Radeon Graphics", want: gpuKindIntegrated},
		{name: "Radeon Graphics", want: gpuKindIntegrated},
		{name: "Radeon 8060S", want: gpuKindIntegrated},
		{name: "Radeon 780M Graphics", want: gpuKindIntegrated},
		{name: "AMD Radeon 890M", want: gpuKindIntegrated},
		{name: "", want: gpuKindDiscrete},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := gpuKindForName(tc.name); got != tc.want {
				t.Errorf("gpuKindForName(%q) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

func TestFormatAMDRegister(t *testing.T) {
	cases := []struct {
		name    string
		entries []amdRegisterEntry
		want    string
	}{
		{
			name:    "empty",
			entries: nil,
			want:    "",
		},
		{
			name: "single integrated",
			entries: []amdRegisterEntry{{
				kind: gpuKindIntegrated, card: "card0", driver: amdDriverName,
				name: "AMD Radeon Graphics", family: "RV", devid: "1636", mem: 0,
			}},
			want: "igpu,card0,amdgpu,AMD Radeon Graphics,RV,1636,0",
		},
		{
			name: "multiple with discrete mem",
			entries: []amdRegisterEntry{
				{kind: gpuKindIntegrated, card: "card0", driver: amdDriverName, name: "AMD Radeon Graphics", family: "RV", devid: "1636", mem: 0},
				{kind: gpuKindDiscrete, card: "card1", driver: amdDriverName, name: "AMD Radeon RX 7900 XTX", family: "GC_11_0_0", devid: "744c", mem: 25757220864},
			},
			want: "igpu,card0,amdgpu,AMD Radeon Graphics,RV,1636,0:dgpu,card1,amdgpu,AMD Radeon RX 7900 XTX,GC_11_0_0,744c,25757220864",
		},
		{
			name:    "unknown model leaves metadata empty",
			entries: []amdRegisterEntry{{kind: gpuKindDiscrete, card: "card0", driver: amdDriverName, mem: 0}},
			want:    "dgpu,card0,amdgpu,,,,0",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatAMDRegister(tc.entries); got != tc.want {
				t.Errorf("formatAMDRegister() = %q, want %q", got, tc.want)
			}
		})
	}
}
