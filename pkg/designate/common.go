/*
Copyright 2022.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package designate

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	mariadbv1 "github.com/openstack-k8s-operators/mariadb-operator/api/v1beta1"
)

// Common static errors for all designate functionality
var (
	// Controller errors
	ErrRedisRequired               = errors.New("unable to configure designate deployment without Redis")
	ErrNetworkAttachmentConfig     = errors.New("not all pods have interfaces with ips as configured in NetworkAttachments")
	ErrNetworkAttachmentNotFound   = errors.New("unable to locate network attachment")
	ErrControlNetworkNotConfigured = errors.New("designate control network attachment not configured, check NetworkAttachments and ControlNetworkName")
	// Package errors
	ErrPredictableIPAllocation     = errors.New("predictable IPs: cannot allocate IP addresses")
	ErrPredictableIPOutOfAddresses = errors.New("predictable IPs: out of available addresses")
	ErrCannotAllocateIPAddresses   = errors.New("cannot allocate IP addresses")
)

const (
	// KollaServiceCommand - the command to start the service binary in the kolla container
	KollaServiceCommand = "/usr/local/bin/kolla_set_configs && /usr/local/bin/kolla_start"
	// DesignateDatabaseName - the name of the DB to store tha API schema
	DesignateDatabaseName = "designate"
)

// GetScriptConfigMapName returns the name of the ConfigMap used for the
// config merger and the service init scripts
func GetScriptConfigMapName(crName string) string {
	return fmt.Sprintf("%s-scripts", crName)
}

// GetServiceConfigConfigMapName returns the name of the ConfigMap used to
// store the service configuration files
func GetServiceConfigConfigMapName(crName string) string {
	return fmt.Sprintf("%s-config-data", crName)
}

// DatabaseStatus -
type DatabaseStatus int

const (
	// DBFailed -
	DBFailed DatabaseStatus = iota
	// DBCreating -
	DBCreating DatabaseStatus = iota
	// DBCompleted -
	DBCompleted DatabaseStatus = iota
)

// MessageBusStatus -
type MessageBusStatus int

const (
	// MQFailed -
	MQFailed MessageBusStatus = iota
	// MQCreating -
	MQCreating MessageBusStatus = iota
	// MQCompleted -
	MQCompleted MessageBusStatus = iota
)

// Database -
type Database struct {
	Database *mariadbv1.Database
	Status   DatabaseStatus
}

// MessageBus -
type MessageBus struct {
	SecretName string
	Status     MessageBusStatus
}

// MultusNetworkConfig represents a single network configuration for Multus CNI
type MultusNetworkConfig struct {
	Name         string   `json:"name"`
	Namespace    string   `json:"namespace,omitempty"`
	Interface    string   `json:"interface,omitempty"`
	IPs          []string `json:"ips,omitempty"`
	MAC          string   `json:"mac,omitempty"`
	DefaultRoute []string `json:"default-route,omitempty"`
}

// CreateMultusAnnotation creates a Multus CNI annotation for pod specifications
func CreateMultusAnnotation(networks []MultusNetworkConfig) (string, error) {
	if len(networks) == 0 {
		return "", nil
	}

	networksJSON, err := json.Marshal(networks)
	if err != nil {
		return "", fmt.Errorf("failed to marshal networks to JSON: %w", err)
	}

	return string(networksJSON), nil
}

// CreatePredictableMultusAnnotation creates a Multus CNI annotation with predictable IPs
func CreatePredictableMultusAnnotation(networkName, interfaceName string, podIndex int, predParams *NADIpam) (string, error) {
	// Calculate predictable IP based on pod index from the IPAM parameters
	ip := predParams.RangeStart
	for i := 0; i < podIndex; i++ {
		ip = ip.Next()
		if ip == predParams.RangeEnd {
			return "", fmt.Errorf("pod index %d exceeds available IP range", podIndex)
		}
	}

	networks := []MultusNetworkConfig{
		{
			Name:      networkName,
			Interface: interfaceName,
			IPs:       []string{ip.String()},
		},
	}

	return CreateMultusAnnotation(networks)
}

// CreatePerPodPredictableAnnotations creates annotations for a specific pod based on its index
func CreatePerPodPredictableAnnotations(networkName, interfaceName string, podIndex int, predParams *NADIpam) (map[string]string, error) {
	annotations := make(map[string]string)

	// Calculate predictable IP for this specific pod
	predictableIP, err := GetPodPredictableIP(predParams, podIndex)
	if err != nil {
		return nil, err
	}

	// Create Multus annotation with the specific IP
	networks := []MultusNetworkConfig{
		{
			Name:      networkName,
			Interface: interfaceName,
			IPs:       []string{predictableIP},
		},
	}

	multusAnnotation, err := CreateMultusAnnotation(networks)
	if err != nil {
		return nil, err
	}

	annotations["k8s.v1.cni.cncf.io/networks"] = multusAnnotation
	annotations["designate.openstack.org/predictable-ip"] = predictableIP
	annotations["designate.openstack.org/pod-index"] = fmt.Sprintf("%d", podIndex)

	return annotations, nil
}

// GeneratePredictableIPMaps creates IP maps for bind and mdns services based on network parameters
func GeneratePredictableIPMaps(predParams *NADIpam, bindReplicas, mdnsReplicas int) (bindMap, mdnsMap map[string]string) {
	bindMap = make(map[string]string)
	mdnsMap = make(map[string]string)

	// Generate bind IPs
	ip := predParams.RangeStart
	for i := 0; i < bindReplicas; i++ {
		if ip == predParams.RangeEnd {
			break // Don't exceed the range
		}
		bindMap[fmt.Sprintf("bind_address_%d", i)] = ip.String()
		ip = ip.Next()
	}

	// Generate mdns IPs
	for i := 0; i < mdnsReplicas; i++ {
		if ip == predParams.RangeEnd {
			break // Don't exceed the range
		}
		mdnsMap[fmt.Sprintf("mdns_address_%d", i)] = ip.String()
		ip = ip.Next()
	}

	return bindMap, mdnsMap
}

// GetPodPredictableIP calculates the predictable IP for a specific pod index
func GetPodPredictableIP(predParams *NADIpam, podIndex int) (string, error) {
	ip := predParams.RangeStart
	for i := 0; i < podIndex; i++ {
		ip = ip.Next()
		if ip == predParams.RangeEnd {
			return "", fmt.Errorf("pod index %d exceeds available IP range", podIndex)
		}
	}
	return ip.String(), nil
}

// ExtractPodIndexFromName extracts the pod index from a StatefulSet pod name
// StatefulSet pods are named as {statefulset-name}-{ordinal}
func ExtractPodIndexFromName(podName string) (int, error) {
	// Find the last dash in the pod name
	lastDashIndex := strings.LastIndex(podName, "-")
	if lastDashIndex == -1 {
		return 0, fmt.Errorf("invalid pod name format: %s", podName)
	}

	// Extract the ordinal part
	ordinalStr := podName[lastDashIndex+1:]
	ordinal, err := strconv.Atoi(ordinalStr)
	if err != nil {
		return 0, fmt.Errorf("invalid ordinal in pod name %s: %w", podName, err)
	}

	return ordinal, nil
}
