### To create the operator (One time thing and does not need to be executed)
```shell
operator-sdk init --plugins=go/v4-alpha --domain=tarkalabs.com --repo=github.com/tarkalabs/namespaced-db-operator
operator-sdk create api --group=db --version=v1alpha1 --kind=DatabaseInstance --resource=true --controller=true
```

### Development setup

#### Setup k3d Cluster
```shell
k3d registry create localhost --port 12345
k3d cluster create mycluster -p "40000:30001@server:0" --registry-use k3d-localhost:12345
```

#### Build and push the controller to the cluster
```shell
make docker-build-debug docker-push IMG="localhost:12345/namespaced-db-operator:debug"
make deploy-debug IMG="k3d-localhost:12345/namespaced-db-operator:debug"

# Expose the deployment for debugging

kubectl expose deployment namespaced-db-operator-controller-manager -n namespaced-db-operator-system --type NodePort --port 40000
kubectl patch svc namespaced-db-operator-controller-manager -n namespaced-db-operator-system --type='json' --patch='[{"op": "replace", "path": "/spec/ports/0/nodePort", "value":30001}]'
```

#### Note
* [After modifying the `*_types.go` file always run the command `make generate` and to update the generated code for that resource type](https://sdk.operatorframework.io/docs/building-operators/golang/tutorial/#define-the-api)
* [Once the API is defined with spec/status fields and CRD validation markers, the CRD manifests can be generated and updated with the command `make manifests`](https://sdk.operatorframework.io/docs/building-operators/golang/tutorial/#generating-crd-manifests)
* [After making changes to the RBAC markers in controller file run `make manifests`](https://sdk.operatorframework.io/docs/building-operators/golang/tutorial/#specify-permissions-and-generate-rbac-manifests)