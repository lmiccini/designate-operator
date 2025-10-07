/*
Copyright 2024 Red Hat Inc.

Licensed under the Apache License, Version 2.0 (the "License"); you may
not use this file except in compliance with the License. You may obtain
a copy of the License at

     http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
License for the specific language governing permissions and limitations
under the License.
*/

package designate

import (
	"context"
	"fmt"
	"net/netip"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// IPSetManager manages IP sets using the original ConfigMap approach
type IPSetManager struct {
	client  client.Client
	scheme  *runtime.Scheme
	setName string
	ns      string
}

// NewIPSetManager creates a new ipset manager using the original ConfigMap approach
func NewIPSetManager(client client.Client, scheme *runtime.Scheme, setName, namespace string) *IPSetManager {
	return &IPSetManager{
		client:  client,
		scheme:  scheme,
		setName: setName,
		ns:      namespace,
	}
}

// handleConfigMap handles ConfigMap creation/retrieval (from original implementation)
func (m *IPSetManager) handleConfigMap(ctx context.Context, configMapName string, labels map[string]string) (*corev1.ConfigMap, error) {
	nodeConfigMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: m.ns,
			Labels:    labels,
		},
		Data: make(map[string]string),
	}

	// Look for existing config map and if exists, read existing data
	foundMap := &corev1.ConfigMap{}
	err := m.client.Get(ctx, types.NamespacedName{Name: configMapName, Namespace: m.ns}, foundMap)
	if err != nil {
		if errors.IsNotFound(err) {
			// ConfigMap doesn't exist, will create new one
		} else {
			return nil, err
		}
	} else {
		// Retrieved existing map, use its data
		nodeConfigMap.Data = foundMap.Data
	}

	return nodeConfigMap, nil
}

// allocatePredictableIPs allocates IPs using the original approach (from original implementation)
func (m *IPSetManager) allocatePredictableIPs(ctx context.Context, predictableIPParams *NADIpam, ipHolders []string, existingMap map[string]string, allocatedIPs map[string]bool) (map[string]string, map[string]bool, error) {
	updatedMap := make(map[string]string)
	var predictableIPsRequired []string

	// First scan existing allocations so we can keep existing allocations
	for _, ipHolder := range ipHolders {
		if ipValue, ok := existingMap[ipHolder]; ok {
			updatedMap[ipHolder] = ipValue
		} else {
			predictableIPsRequired = append(predictableIPsRequired, ipHolder)
		}
	}

	// Get new IPs using the range from predictableIPParams minus the allocatedIPs
	for _, nodeName := range predictableIPsRequired {
		ipAddress, err := GetNextIP(predictableIPParams, allocatedIPs)
		if err != nil {
			return nil, nil, err
		}
		updatedMap[nodeName] = ipAddress
	}

	return updatedMap, allocatedIPs, nil
}

// AllocateIP allocates an IP using the original ConfigMap approach
func (m *IPSetManager) AllocateIP(ipRange netip.Prefix, podIndex int) (string, error) {
	// Convert netip.Prefix to NADIpam format for compatibility with original functions
	networkParams := &NetworkParameters{
		CIDR: ipRange,
	}
	predictableIPParams, err := GetPredictableIPAM(networkParams)
	if err != nil {
		return "", fmt.Errorf("failed to get predictable IP parameters: %w", err)
	}

	// Create pod key in the format used by the original implementation
	podKey := fmt.Sprintf("pod_%d", podIndex)
	ipHolders := []string{podKey}

	// Get existing ConfigMap
	labels := map[string]string{
		"app.kubernetes.io/name":      "designate",
		"app.kubernetes.io/component": "ipset",
	}
	configMap, err := m.handleConfigMap(context.TODO(), m.setName, labels)
	if err != nil {
		return "", fmt.Errorf("failed to handle ConfigMap: %w", err)
	}

	// Get allocated IPs from other ConfigMaps to avoid conflicts
	allocatedIPs := make(map[string]bool)

	// Check bind ConfigMap
	bindConfigMap := &corev1.ConfigMap{}
	err = m.client.Get(context.TODO(), types.NamespacedName{Name: BindPredIPConfigMap, Namespace: m.ns}, bindConfigMap)
	if err == nil {
		for _, predIP := range bindConfigMap.Data {
			allocatedIPs[predIP] = true
		}
	}

	// Check mdns ConfigMap
	mdnsConfigMap := &corev1.ConfigMap{}
	err = m.client.Get(context.TODO(), types.NamespacedName{Name: MdnsPredIPConfigMap, Namespace: m.ns}, mdnsConfigMap)
	if err == nil {
		for _, predIP := range mdnsConfigMap.Data {
			allocatedIPs[predIP] = true
		}
	}

	// Allocate IPs
	updatedMap, _, err := m.allocatePredictableIPs(context.TODO(), predictableIPParams, ipHolders, configMap.Data, allocatedIPs)
	if err != nil {
		return "", fmt.Errorf("failed to allocate predictable IPs: %w", err)
	}

	// Update ConfigMap
	configMap.Data = updatedMap
	_, err = controllerutil.CreateOrPatch(context.TODO(), m.client, configMap, func() error {
		configMap.Labels = labels
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("failed to create or patch ConfigMap: %w", err)
	}

	// Return the allocated IP for this pod
	if allocatedIP, exists := updatedMap[podKey]; exists {
		return allocatedIP, nil
	}

	return "", fmt.Errorf("failed to allocate IP for pod %d", podIndex)
}

// IsIPAllocated checks if an IP is already allocated
func (m *IPSetManager) IsIPAllocated(ip string) bool {
	configMap := &corev1.ConfigMap{}
	err := m.client.Get(context.TODO(), types.NamespacedName{Name: m.setName, Namespace: m.ns}, configMap)
	if err != nil {
		return false
	}

	for _, existingIP := range configMap.Data {
		if existingIP == ip {
			return true
		}
	}
	return false
}

// ReleaseIP removes an IP from the set
func (m *IPSetManager) ReleaseIP(ip string) error {
	configMap := &corev1.ConfigMap{}
	err := m.client.Get(context.TODO(), types.NamespacedName{Name: m.setName, Namespace: m.ns}, configMap)
	if err != nil {
		return err
	}

	// Find and remove the IP from ConfigMap data
	for key, existingIP := range configMap.Data {
		if existingIP == ip {
			delete(configMap.Data, key)
			return m.client.Update(context.TODO(), configMap)
		}
	}
	return nil // IP not found, nothing to do
}
