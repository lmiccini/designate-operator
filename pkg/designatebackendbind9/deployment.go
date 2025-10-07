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

package designatebackendbind9

import (
	"fmt"

	designatev1beta1 "github.com/openstack-k8s-operators/designate-operator/api/v1beta1"
	designate "github.com/openstack-k8s-operators/designate-operator/pkg/designate"
	topologyv1 "github.com/openstack-k8s-operators/infra-operator/apis/topology/v1beta1"
	common "github.com/openstack-k8s-operators/lib-common/modules/common"
	"github.com/openstack-k8s-operators/lib-common/modules/common/affinity"
	"github.com/openstack-k8s-operators/lib-common/modules/common/env"

	// labels "github.com/openstack-k8s-operators/lib-common/modules/common/labels"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// TLSe notes! : the communication with the bind instances are currently not encrypted so there is no reason to mount
// certs etc here.

const (
	// PVCSuffix is the suffix used for PVC names
	PVCSuffix = "-designate-bind"
)

// StatefulSet creates a StatefulSet for the designate backend bind9 service
func StatefulSet(
	instance *designatev1beta1.DesignateBackendbind9,
	configHash string,
	labels map[string]string,
	annotations map[string]string,
	topology *topologyv1.Topology,
) (*appsv1.StatefulSet, error) {

	// TODO(beagles): Dbl check that running as the default kolla/tcib user works okay here. Permissions on some of the
	// directories require serious care.

	livenessProbe := &corev1.Probe{
		// TODO might need tuning
		TimeoutSeconds:      15,
		PeriodSeconds:       13,
		InitialDelaySeconds: 15,
	}
	readinessProbe := &corev1.Probe{
		// TODO might need tuning
		TimeoutSeconds:      15,
		PeriodSeconds:       13,
		InitialDelaySeconds: 10,
	}

	// TODO(beagles): implement an rndc shutdown command to bring the pod down gracefully!

	// Check for the rndc port.
	livenessProbe.TCPSocket = &corev1.TCPSocketAction{
		Port: intstr.IntOrString{Type: intstr.Int, IntVal: int32(953)},
	}
	readinessProbe.TCPSocket = &corev1.TCPSocketAction{
		Port: intstr.IntOrString{Type: intstr.Int, IntVal: int32(953)},
	}

	// Parse the storageRequest defined in the CR
	storageRequest, err := resource.ParseQuantity(instance.Spec.StorageRequest)
	if err != nil {
		return nil, err
	}

	envVars := map[string]env.Setter{}
	envVars["KOLLA_CONFIG_STRATEGY"] = env.SetValue("COPY_ALWAYS")
	envVars["CONFIG_HASH"] = env.SetValue(configHash)

	serviceVolumes := getServicePodVolumes(instance.Name)

	serviceName := fmt.Sprintf("%s-backendbind9", designate.ServiceName)
	statefulSet := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.Name,
			Namespace: instance.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.StatefulSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Replicas: instance.Spec.Replicas,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: annotations,
					Labels:      labels,
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: instance.Spec.ServiceAccount,
					Volumes:            serviceVolumes,
					Containers: []corev1.Container{
						{
							Name:           serviceName,
							Image:          instance.Spec.ContainerImage,
							Env:            env.MergeEnvs([]corev1.EnvVar{}, envVars),
							VolumeMounts:   getServicePodVolumeMounts(instance.Name + PVCSuffix),
							Resources:      instance.Spec.Resources,
							LivenessProbe:  livenessProbe,
							ReadinessProbe: readinessProbe,
						},
					},
				},
			},
		},
	}

	statefulSet.Spec.PersistentVolumeClaimRetentionPolicy = &appsv1.StatefulSetPersistentVolumeClaimRetentionPolicy{
		WhenDeleted: appsv1.DeletePersistentVolumeClaimRetentionPolicyType,
		WhenScaled:  appsv1.RetainPersistentVolumeClaimRetentionPolicyType,
	}
	blockOwnerDeletion := false
	ownerRef := metav1.NewControllerRef(instance, instance.GroupVersionKind())
	ownerRef.BlockOwnerDeletion = &blockOwnerDeletion

	statefulSet.Spec.VolumeClaimTemplates = []corev1.PersistentVolumeClaim{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:            instance.Name + PVCSuffix,
				Namespace:       instance.Namespace,
				Labels:          labels,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{
					corev1.ReadWriteOnce,
				},
				StorageClassName: &instance.Spec.StorageClass,
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: storageRequest,
					},
				},
			},
		},
	}

	if instance.Spec.NodeSelector != nil {
		statefulSet.Spec.Template.Spec.NodeSelector = *instance.Spec.NodeSelector
	}

	if topology != nil {
		topology.ApplyTo(&statefulSet.Spec.Template)
	} else {
		// If possible two pods of the same service should not
		// run on the same worker node. If this is not possible
		// the get still created on the same worker node.
		statefulSet.Spec.Template.Spec.Affinity = affinity.DistributePods(
			common.AppSelector,
			[]string{
				serviceName,
			},
			corev1.LabelHostname,
		)
	}
	// If possible two pods of the same service should not run on the same worker node. If this is not possible they
	// will be scheduled on the same node. Where the bind servers are stateful, it's best to have them all available
	// even if they are on the same host.
	statefulSet.Spec.Template.Spec.Affinity = affinity.DistributePods(
		common.AppSelector,
		[]string{
			serviceName,
		},
		corev1.LabelHostname,
	)
	// TODO: bind's init container doesn't need most of this stuff. It doesn't use rabbitmq, redis or access the
	// database. Should clean this up!
	envVars = map[string]env.Setter{}
	envVars["POD_NAME"] = env.DownwardAPI("metadata.name")
	envVars["POD_NAMESPACE"] = env.DownwardAPI("metadata.namespace")
	envVars["CustomConf"] = env.SetValue(common.CustomServiceConfigFileName)
	envVars["RNDC_PREFIX"] = env.SetValue(designate.DesignateRndcKey)
	// NETWORK_ATTACHMENT_DEFINITION will be set by the controller

	// Add predictable IP from pod annotation using downward API
	predictableIPEnvVar := corev1.EnvVar{
		Name: "PREDICTABLE_IP_FROM_ANNOTATION",
		ValueFrom: &corev1.EnvVarSource{
			FieldRef: &corev1.ObjectFieldSelector{
				FieldPath: "metadata.annotations['designate.openstack.org/predictable-ip']",
			},
		},
	}

	env := env.MergeEnvs([]corev1.EnvVar{predictableIPEnvVar}, envVars)
	initContainerDetails := designate.InitContainerDetails{
		ContainerImage: instance.Spec.ContainerImage,
		VolumeMounts:   getInitVolumeMounts(),
		EnvVars:        env,
	}
	statefulSet.Spec.Template.Spec.InitContainers = []corev1.Container{
		designate.SimpleInitContainer(initContainerDetails),
	}

	return statefulSet, nil
}
