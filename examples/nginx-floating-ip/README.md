# Example: Nginx with a Floating Public IP

This example deploys an Nginx pod and assigns a floating OCI public IP to it via a `PivotIP` resource. The IP is pinned to nodes in a specific availability domain.

## Apply

```sh
kubectl apply -f .
```

## What this creates

- A `Deployment` running one Nginx pod
- A `Service` of type `ClusterIP` selecting that pod
- A `PivotIP` that reserves an OCI public IP and routes traffic on port 80 to the Service

## Check status

```sh
kubectl get pivotip nginx-ip
# NAME       PUBLIC IP    NODE           SERVICE    AGE
# nginx-ip   1.2.3.4      node-worker1   nginx      30s

curl http://$(kubectl get pivotip nginx-ip -o jsonpath='{.status.publicIP}')
```

## Cleanup

```sh
kubectl delete -f .
```

The controller removes the OCI reserved public IP and secondary private IP automatically when the `PivotIP` is deleted.
