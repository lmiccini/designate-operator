/*
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
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// ServicePort represents a port to expose on a Service
type ServicePort struct {
	Name string
	Port int32
}

// PodService creates a ClusterIP Service for a specific pod in a StatefulSet
// This provides a stable IP address for the pod that can be used in configuration
func PodService(
	name string,
	namespace string,
	labels map[string]string,
	podName string,
	ports []ServicePort,
) *corev1.Service {
	// Create selector that matches the specific pod
	// Only use statefulset.kubernetes.io/pod-name since it's unique and automatically set by K8s
	selector := map[string]string{
		"statefulset.kubernetes.io/pod-name": podName,
	}

	servicePorts := make([]corev1.ServicePort, len(ports))
	for i, p := range ports {
		servicePorts[i] = corev1.ServicePort{
			Name:       p.Name,
			Protocol:   corev1.ProtocolTCP,
			Port:       p.Port,
			TargetPort: intstr.FromInt(int(p.Port)),
		}
	}

	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: selector,
			Ports:    servicePorts,
		},
	}
}

// MdnsServiceName returns the service name for an MDNS pod
func MdnsServiceName(baseName string, index int) string {
	return fmt.Sprintf("%s-%d-svc", baseName, index)
}

// BindServiceName returns the service name for a Bind pod
func BindServiceName(baseName string, index int) string {
	return fmt.Sprintf("%s-%d-svc", baseName, index)
}

// MdnsPodName returns the pod name for an MDNS StatefulSet pod
func MdnsPodName(baseName string, index int) string {
	return fmt.Sprintf("%s-%d", baseName, index)
}

// BindPodName returns the pod name for a Bind StatefulSet pod
func BindPodName(baseName string, index int) string {
	return fmt.Sprintf("%s-%d", baseName, index)
}
