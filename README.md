# k8s-node-tagger

A Kubernetes controller that watches Kubernetes Nodes and copies labels from the node to the cloud provider's VM as tags (AWS) or labels (GCP).

## Testing

- lint: `make lint`
- test: `make test`

Running it locally is possible too. The standard mechanisms for finding the current kubeconfig and aws/gcp credentials are used.

Assuming you use `~/.aws/config` and have profiles defined you could run:

```console
AWS_PROFILE=my-profile AWS_REGION=region go run -v .
```

For GCP you want to ensure you have application default credentials setup by running either `gcloud auth login --update-adc` or `gcloud auth application-default login`.

## Inspiration

Inspired by [mtougeron/k8s-pvc-tagger](https://github.com/mtougeron/k8s-pvc-tagger) which provides similar functionality for copying PVC labels to the underlying cloud provider's disk.