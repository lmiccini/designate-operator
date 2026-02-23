/*
Licensed under the Apache License, Version 2.0 (the "License");
@you may not use this file except in compliance with the License.
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
	"fmt"
	"net/netip"

	networkv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
)

// NetworkParameters - Parameters for the Designate networks, based on the config of the NAD
type NetworkParameters struct {
	CIDR                    netip.Prefix
	ProviderAllocationStart netip.Addr
	ProviderAllocationEnd   netip.Addr
}

// NADConfig - Network parameters of the NAD
// Supports both bridge+whereabouts (ipam field) and ovn-k8s-cni-overlay (subnets field)
type NADConfig struct {
	Type    string   `json:"type"`
	IPAM    NADIpam  `json:"ipam"`
	Subnets string   `json:"subnets"` // For OVN overlay: "192.168.88.0/24"
}

// NADIpam represents network attachment definition IPAM configuration
type NADIpam struct {
	CIDR       netip.Prefix `json:"range"`
	RangeStart netip.Addr   `json:"range_start"`
	RangeEnd   netip.Addr   `json:"range_end"`
}

// GetNADConfig parses and returns the NAD configuration from a NetworkAttachmentDefinition
// Supports both bridge+whereabouts and ovn-k8s-cni-overlay NAD types
func GetNADConfig(
	nad *networkv1.NetworkAttachmentDefinition,
) (*NADConfig, error) {
	nadConfig := &NADConfig{}
	jsonDoc := []byte(nad.Spec.Config)
	err := json.Unmarshal(jsonDoc, nadConfig)
	if err != nil {
		return nil, err
	}
	return nadConfig, nil
}

// isOVNOverlay checks if the NAD is using ovn-k8s-cni-overlay
func isOVNOverlay(nadConfig *NADConfig) bool {
	return nadConfig.Type == "ovn-k8s-cni-overlay"
}

// GetNetworkParametersFromNAD - Extract network information from the Network Attachment Definition
// Supports both bridge+whereabouts and ovn-k8s-cni-overlay NAD types (single subnet only)
func GetNetworkParametersFromNAD(
	nad *networkv1.NetworkAttachmentDefinition,
) (*NetworkParameters, error) {
	networkParameters := &NetworkParameters{}

	nadConfig, err := GetNADConfig(nad)
	if err != nil {
		return nil, fmt.Errorf("cannot read network parameters: %w", err)
	}

	var cidr netip.Prefix
	var rangeEnd netip.Addr

	if isOVNOverlay(nadConfig) {
		// OVN overlay format - parse subnet from "subnets" field
		if nadConfig.Subnets == "" {
			return nil, fmt.Errorf("OVN overlay NAD missing subnets field")
		}

		// Parse the subnet (single subnet only, no comma-separated lists)
		cidr, err = netip.ParsePrefix(nadConfig.Subnets)
		if err != nil {
			return nil, fmt.Errorf("failed to parse OVN subnet %q: %w", nadConfig.Subnets, err)
		}

		// For OVN overlay, we don't have explicit range_start/range_end
		// Reserve the upper portion of the subnet for predictable IPs
		// For a /24 network, start predictable IPs at .200 (leaving .1-.199 for pod allocation)
		addr := cidr.Addr()
		rangeEnd = addr

		// Try to start at .200 for /24 networks, or .30 for smaller subnets
		targetOffset := 199
		for i := 0; i < targetOffset; i++ {
			rangeEnd = rangeEnd.Next()
			if !cidr.Contains(rangeEnd) {
				// Subnet too small for .200, fall back to .30
				rangeEnd = addr
				for j := 0; j < 29; j++ {
					rangeEnd = rangeEnd.Next()
				}
				break
			}
		}
	} else {
		// Bridge + whereabouts format - use ipam fields
		cidr = nadConfig.IPAM.CIDR
		rangeEnd = nadConfig.IPAM.RangeEnd
	}

	networkParameters.CIDR = cidr

	// Allocate IP addresses for predictable IPs (mdns and bind servers)
	// The range starts right after the pod allocation range
	networkParameters.ProviderAllocationStart = rangeEnd.Next()
	end := networkParameters.ProviderAllocationStart
	for range BindProvPredictablePoolSize {
		if !networkParameters.CIDR.Contains(end) {
			return nil, fmt.Errorf("%w: %d IP addresses in %s", ErrCannotAllocateIPAddresses, BindProvPredictablePoolSize, networkParameters.CIDR)
		}
		end = end.Next()
	}
	networkParameters.ProviderAllocationEnd = end

	return networkParameters, nil
}
