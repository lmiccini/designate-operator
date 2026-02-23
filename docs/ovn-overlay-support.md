# OVN Overlay Network Support

## Overview

The designate-operator supports two types of network-attachment-definitions (NADs):

1. **OVN-K8s CNI Overlay** (`ovn-k8s-cni-overlay`) - Recommended for OpenShift
2. **Bridge + Whereabouts** (`bridge` with `whereabouts` IPAM) - Traditional approach

The operator automatically detects the NAD type and handles configuration accordingly.

## OVN Overlay Configuration (Recommended)

### Example NAD

```yaml
apiVersion: k8s.cni.cncf.io/v1
kind: NetworkAttachmentDefinition
metadata:
  name: designate
  namespace: openstack
spec:
  config: |
    {
      "cniVersion": "0.3.1",
      "name": "designate",
      "type": "ovn-k8s-cni-overlay",
      "topology": "layer2",
      "netAttachDefName": "openstack/designate",
      "subnets": "192.168.88.0/24"
    }
```

### IP Allocation

- **Pod IPs**: `192.168.88.1` - `192.168.88.199` (managed by OVN)
- **Predictable IPs**: `192.168.88.200` - `192.168.88.225` (mdns and bind servers)

All designate components (workers, mdns, bind9) use the same subnet.

### Benefits

✅ **Native Integration**: Built into OpenShift/Kubernetes
✅ **Better Performance**: Kernel-based networking
✅ **Simpler Setup**: No IPAM plugin configuration needed
✅ **Production Ready**: Fully supported in OpenShift environments

## Bridge + Whereabouts Configuration (Legacy)

### Example NAD

```yaml
apiVersion: k8s.cni.cncf.io/v1
kind: NetworkAttachmentDefinition
metadata:
  name: designate
  namespace: openstack
spec:
  config: |
    {
      "cniVersion": "0.3.1",
      "name": "designate",
      "type": "bridge",
      "bridge": "designate",
      "ipam": {
        "type": "whereabouts",
        "range": "192.168.1.0/24",
        "range_start": "192.168.1.30",
        "range_end": "192.168.1.70"
      }
    }
```

### IP Allocation

- **Pod IPs**: `192.168.1.30` - `192.168.1.70` (whereabouts IPAM)
- **Predictable IPs**: `192.168.1.71` - `192.168.1.96` (mdns and bind servers)

## Migration to OVN Overlay

To migrate from bridge+whereabouts to OVN overlay:

1. **Create OVN NAD:**
   ```bash
   kubectl apply -f designate-nad-ovn.yaml
   ```

2. **Recreate pods:**
   ```bash
   # Pods will automatically use the new NAD
   kubectl delete pods -l service=designate -n openstack
   ```

No changes are required to the Designate CR - the operator automatically detects the NAD type.

## Troubleshooting

### Verify NAD Type

```bash
# Check NAD configuration
kubectl get nad designate -n openstack -o jsonpath='{.spec.config}' | jq .

# Look for "type" field:
# - "ovn-k8s-cni-overlay" = OVN overlay
# - "bridge" = Bridge + whereabouts
```

### Check Pod Networks

```bash
# View pod network annotations
kubectl get pod <pod-name> -n openstack -o jsonpath='{.metadata.annotations.k8s\.v1\.cni\.cncf\.io/network-status}' | jq .
```

### Verify Predictable IPs

```bash
# Check configmaps
kubectl get configmap -n openstack | grep designate

# View mdns IPs
kubectl get configmap designate-mdns-predictable-ips -n openstack -o yaml

# View bind IPs
kubectl get configmap designate-bind-predictable-ips -n openstack -o yaml
```

## Use Cases

### BGP-Advertised DNS

OVN overlay works well with BGP for DNS services. Configure your BGP router to advertise the designate subnet (`192.168.88.0/24` in the example above) to external networks.

### Multi-Tenant Isolation

Use OpenShift network policies with OVN overlay to provide additional network isolation between tenants while still allowing designate to function properly.
