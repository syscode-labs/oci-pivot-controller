# oci-pivot-controller

[![CI](https://github.com/syscode-labs/oci-pivot-controller/actions/workflows/ci.yml/badge.svg)](https://github.com/syscode-labs/oci-pivot-controller/actions/workflows/ci.yml)
[![CodeQL](https://github.com/syscode-labs/oci-pivot-controller/actions/workflows/codeql.yml/badge.svg)](https://github.com/syscode-labs/oci-pivot-controller/actions/workflows/codeql.yml)
[![Go Version](https://img.shields.io/badge/go-1.25-00ADD8?logo=go)](https://go.dev/doc/devel/release)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)

**oci-pivot-controller is a Kubernetes controller that gives your cluster a stable floating public IP on OCI — and automatically moves it to a healthy node if the current one goes down.**

In plain terms: you create a `PivotIP` resource pointing at a Service, and the controller reserves a real OCI public IP address and wires it up so traffic reaches your app. If the node holding that IP becomes unhealthy, the controller detects it, picks a better node, and re-routes traffic — without you doing anything. The IP address never changes.

It works entirely through OCI's native networking (secondary private IPs + reserved public IPs). No load balancer, no cloud provider integration, no CNI changes required.

## What It Manages

- `PivotIP`: assigns and maintains a floating OCI reserved public IP for a Kubernetes Service

## How It Works

1. You create a `PivotIP` pointing at a Service.
2. The controller picks the healthiest node (fewest existing assignments, must be Ready).
3. It creates a secondary private IP on that node's VNIC and attaches a reserved OCI public IP to it.
4. It sets `Service.spec.externalIPs` so the node intercepts and routes inbound traffic to your pods.
5. When the node becomes unhealthy, the controller moves the secondary private IP to a new node and reassigns the public IP — the address stays the same.

## Prerequisites

- Kubernetes cluster running on OCI Compute instances (Ampere A1 or x86)
- OCI IAM policy granting the controller permission to manage private and public IPs (see below)
- `helm` (recommended) or `kubectl`

### Required OCI IAM Policy

The controller uses [instance principal authentication](https://docs.oracle.com/en-us/iaas/Content/Identity/Tasks/callingservicesfrominstances.htm) — no API key needed. Add this policy to the dynamic group that contains your cluster nodes:

```
Allow dynamic-group <your-node-group> to manage private-ips in compartment <your-compartment>
Allow dynamic-group <your-node-group> to manage public-ips in compartment <your-compartment>
```

## Quickstart

### Install with Helm (Recommended)

```sh
helm upgrade --install oci-pivot-controller \
  oci://ghcr.io/syscode-labs/charts/oci-pivot-controller \
  --version <version> \
  -n oci-pivot-system --create-namespace \
  --set oci.compartmentId=ocid1.compartment.oc1..example

kubectl -n oci-pivot-system get pods
```

### Install with Kustomize

```sh
make install
make deploy IMG=ghcr.io/syscode-labs/oci-pivot-controller:<tag>
```

### Uninstall

```sh
helm uninstall oci-pivot-controller -n oci-pivot-system
# or (kustomize path)
make undeploy && make uninstall
```

## First Floating IP in 5 Minutes

Create a Service and a PivotIP in the `default` namespace:

```sh
kubectl apply -f - <<'EOF'
apiVersion: v1
kind: Service
metadata:
  name: my-app
  namespace: default
spec:
  selector:
    app: my-app
  ports:
    - port: 80
      targetPort: 8080
---
apiVersion: pivot.oci.io/v1alpha1
kind: PivotIP
metadata:
  name: my-app-ip
  namespace: default
spec:
  serviceRef:
    name: my-app
EOF
```

Check status:

```sh
kubectl get pivotip -n default
# NAME         PUBLIC IP    NODE           SERVICE    AGE
# my-app-ip    1.2.3.4      node-worker1   my-app     30s

kubectl describe pivotip my-app-ip -n default
```

The `PUBLIC IP` column shows the OCI reserved public IP. Traffic to that address on port 80 reaches your app pods via the elected node.

## PivotIP Reference

```yaml
apiVersion: pivot.oci.io/v1alpha1
kind: PivotIP
metadata:
  name: my-app-ip
  namespace: default
spec:
  # Service in the same namespace to attach the floating IP to. Required.
  serviceRef:
    name: my-app

  # Only nodes matching these labels are eligible to hold the IP.
  # If omitted, all Ready nodes are eligible.
  nodeSelector:
    topology.kubernetes.io/zone: uk-london-1-ad-1

  # OCI compartment OCID for creating IP resources.
  # Defaults to the --compartment-id flag set on the controller.
  compartmentId: ocid1.compartment.oc1..example
```

## Examples

See [`examples/`](examples/) for complete real-world use cases:

- [`examples/nginx-floating-ip/`](examples/nginx-floating-ip/) — Nginx exposed with a floating public IP, zone-pinned to a specific availability domain

## Contributing

Pull requests welcome. Run `make help` for all available targets.

```sh
make test     # run unit + integration tests
make lint     # lint
make build    # compile
```

## License

Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
