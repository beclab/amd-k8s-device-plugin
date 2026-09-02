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
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ROCm/k8s-device-plugin/internal/pkg/amdgpu"
	"github.com/golang/glog"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	// nodeAMDRegisterAnnotation is written on the node this plugin runs on.
	// Its value is a ':'-separated list of per-card entries, each of which is
	// a ','-separated tuple
	// "<igpu|dgpu>,<cardN>,<amdgpu>,<name>,<family>,<devid>,<mem>".
	nodeAMDRegisterAnnotation = "bytetrade.io/node-amd-register"

	// nodeNameEnvVar carries the name of the node the pod runs on. It must be
	// injected via the downward API (spec.nodeName) in the DaemonSet.
	nodeNameEnvVar = "NODE_NAME"

	// nodeRegisterResyncPeriod bounds how often we re-check the node
	// annotation even without a device-count change, so an externally wiped
	// annotation is eventually restored. No-op writes are still skipped.
	nodeRegisterResyncPeriod = 60 * time.Second

	gpuKindIntegrated = "igpu"
	gpuKindDiscrete   = "dgpu"
	amdDriverName     = "amdgpu"
)

// amdIgpuNameRe covers current AMD APU integrated GPU marketing names:
//   - Radeon 8060S / 8050S (Strix Halo)
//   - 890M / 880M / 860M / 840M / 780M / 760M / 740M (Ryzen mobile APU iGPUs)
//   - generic "Radeon Graphics" / "AMD Radeon Graphics" (older APUs)
//
// Matches the prometheus metricRelabelings regex used to drop iGPU metrics.
var amdIgpuNameRe = regexp.MustCompile(`(.*(?:8060S|8050S|890M|880M|860M|840M|780M|760M|740M).*|^(AMD )?Radeon Graphics$)`)

// amdRegisterEntry describes a single AMD GPU card discovered during scan.
type amdRegisterEntry struct {
	kind   string // igpu | dgpu
	card   string // e.g. card0
	driver string // amdgpu
	name   string // product name
	family string // e.g. NV, GC_11_0_0
	devid  string // PCI device id without 0x prefix
	mem    uint64 // discrete VRAM in bytes; 0 for integrated
}

func gpuKindForName(name string) string {
	if name == "" {
		return gpuKindDiscrete
	}
	if amdIgpuNameRe.MatchString(name) {
		return gpuKindIntegrated
	}
	return gpuKindDiscrete
}

func cardProductName(cardName string) string {
	prodnamePath := fmt.Sprintf("/sys/class/drm/%s/device/product_name", cardName)
	if b, err := os.ReadFile(prodnamePath); err == nil {
		if name := strings.TrimSpace(string(b)); name != "" {
			return name
		}
	}

	name, err := amdgpu.GetCardProductName(cardName)
	if err != nil {
		glog.Warningf("node-register: can't read product name for %s: %v", cardName, err)
		return ""
	}
	return strings.TrimSpace(name)
}

func buildAMDRegister() []amdRegisterEntry {
	gpus := amdgpu.GetAMDGPUs()
	entries := make([]amdRegisterEntry, 0, len(gpus))

	for _, device := range gpus {
		cardName := fmt.Sprintf("card%d", device["card"].(int))
		name := cardProductName(cardName)
		kind := gpuKindForName(name)
		if name == "" {
			glog.Warningf("node-register: empty product name for %s, defaulting kind to %s", cardName, gpuKindDiscrete)
		}

		family := ""
		if f, err := amdgpu.GetCardFamilyName(cardName); err != nil {
			glog.Warningf("node-register: can't read family for %s: %v", cardName, err)
		} else {
			family = f
		}

		devid := ""
		if d, err := amdgpu.CardPCIDeviceID(cardName); err != nil {
			glog.Warningf("node-register: can't read PCI device id for %s: %v", cardName, err)
		} else {
			devid = d
		}

		var mem uint64
		if kind != gpuKindIntegrated {
			mem = amdgpu.CardVRAMBytes(device["renderD"].(int))
		}

		entries = append(entries, amdRegisterEntry{
			kind:   kind,
			card:   cardName,
			driver: amdDriverName,
			name:   name,
			family: family,
			devid:  devid,
			mem:    mem,
		})
	}

	return entries
}

func formatAMDRegister(entries []amdRegisterEntry) string {
	parts := make([]string, 0, len(entries))
	for _, e := range entries {
		parts = append(parts, strings.Join([]string{
			e.kind,
			e.card,
			e.driver,
			e.name,
			e.family,
			e.devid,
			strconv.FormatUint(e.mem, 10),
		}, ","))
	}
	return strings.Join(parts, ":")
}

func nodeNameFromEnv() string {
	for _, key := range []string{nodeNameEnvVar, "DS_NODE_NAME"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return ""
}

// resolveNodeName returns the Kubernetes node this plugin pod runs on.
// Prefer NODE_NAME (or DS_NODE_NAME for labeller compatibility) injected via
// the downward API. When unset, fall back to reading this pod's spec.nodeName.
func resolveNodeName(ctx context.Context, cs kubernetes.Interface) (string, error) {
	if name := nodeNameFromEnv(); name != "" {
		return name, nil
	}

	podName := strings.TrimSpace(os.Getenv("HOSTNAME"))
	if podName == "" {
		return "", fmt.Errorf("%s/DS_NODE_NAME unset and HOSTNAME empty", nodeNameEnvVar)
	}

	nsBytes, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		return "", fmt.Errorf("%s/DS_NODE_NAME unset and cannot read pod namespace: %w", nodeNameEnvVar, err)
	}
	podNS := strings.TrimSpace(string(nsBytes))

	pod, err := cs.CoreV1().Pods(podNS).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("%s/DS_NODE_NAME unset and cannot get pod %s/%s: %w", nodeNameEnvVar, podNS, podName, err)
	}
	if pod.Spec.NodeName == "" {
		return "", fmt.Errorf("pod %s/%s has empty spec.nodeName", pod.Namespace, pod.Name)
	}

	glog.Infof("node-register: resolved node name %q from pod %s/%s (set %s via downward API to skip this lookup)",
		pod.Spec.NodeName, pod.Namespace, pod.Name, nodeNameEnvVar)
	return pod.Spec.NodeName, nil
}

func runNodeRegister(ctx context.Context) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		glog.Warningf("node-register: can't build in-cluster config, skipping: %v", err)
		return
	}

	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		glog.Warningf("node-register: can't build clientset, skipping: %v", err)
		return
	}

	nodeName, err := resolveNodeName(ctx, clientset)
	if err != nil {
		glog.Warningf("node-register: cannot resolve node name, skipping node annotation publishing: %v", err)
		return
	}

	ticker := time.NewTicker(nodeRegisterResyncPeriod)
	defer ticker.Stop()

	var (
		lastWritten string
		haveWritten bool
	)

	reconcile := func() {
		value := formatAMDRegister(buildAMDRegister())
		if haveWritten && value == lastWritten {
			return
		}

		if err := patchNodeAMDRegister(ctx, clientset, nodeName, value); err != nil {
			glog.Warningf("node-register: failed to patch node %s annotation: %v", nodeName, err)
			return
		}

		glog.Infof("node-register: updated %s on node %s = %q", nodeAMDRegisterAnnotation, nodeName, value)
		lastWritten = value
		haveWritten = true
	}

	reconcile()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reconcile()
		}
	}
}

func patchNodeAMDRegister(ctx context.Context, clientset kubernetes.Interface, nodeName, value string) error {
	var annotationValue interface{}
	if value == "" {
		annotationValue = nil
	} else {
		annotationValue = value
	}

	patch := map[string]interface{}{
		"metadata": map[string]interface{}{
			"annotations": map[string]interface{}{
				nodeAMDRegisterAnnotation: annotationValue,
			},
		},
	}

	data, err := json.Marshal(patch)
	if err != nil {
		return err
	}

	_, err = clientset.CoreV1().Nodes().Patch(ctx, nodeName, types.MergePatchType, data, metav1.PatchOptions{})
	return err
}
