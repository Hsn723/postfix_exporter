# postfix-exporter Helm Chart

## Quick start

### Add the Helm Repository
```sh
helm repo add postfix-exporter https://hsn723.github.io/postfix_exporter
helm repo update
```

### Install the Chart

Installing the chart with default settings (standalone):

```sh
helm install --create-namespace --namespace postfix-exporter postfix-exporter postfix-exporter/postfix-exporter
```

Specify parameters using `--set key=value[,key=value]` arguments to `helm install`, or provide your own `values.yaml`:

```sh
helm install --create-namespace --namespace postfix-exporter postfix-exporter -f values.yaml postfix-exporter/postfix-exporter
```

## Values
| Key                                      | Type   | Default                                     | Description                               |
|------------------------------------------|--------|---------------------------------------------|-------------------------------------------|
| serviceAccountName                       | string | "postfix"                                   | The name for the service account          |
| postfixServiceName                       | string | ""                                          | The name for the postfix Service          |
| useTCPShowq                              | bool   | `true`                                      | The name for the postfix Service          |
| createRbac                               | bool   | `true`                                      | Whether to create RBAC resources          |
| image.repository                         | string | `"ghcr.io/hsn723/postfix_exporter"`         | Image repository to use                   |
| image.tag                                | string | `{{ .Chart.AppVersion }}`                   | Image tag to use                          |
| image.pullPolicy                         | string | "Always"                                    | Image pullPolicy                          |
| image.pullSecrets                        | list   | `[]`                                        | Image pull secret(s)                      |
| deployment.replicas                      | int    | `2`                                         | Number of controller Pod replicas         |
| deployment.metricsPort                   | int    | `9154`                                      | The metrics server port                   |
| deployment.resources                     | object | `{"requests":{"cpu":100m,"memory":"20Mi"}}` | Resources requested for Deployment        |
| deployment.terminationGracePeriodSeconds | int    | `10`                                        | terminationGracePeriodSeconds for the Pod |
| deployment.extraArgs                     | list   | `[]`                                        |   Additional arguments for the controller |


## Generate Manifests
```sh
helm template --namespace postfix_exporter postfix_exporter [-f values.yaml] postfix_exporter/postfix_exporter
```
