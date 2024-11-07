# k8s-node-tagger

A Kubernetes controller that watches Kubernetes Nodes and copies labels from the node to the cloud provider's VM as tags (AWS) or labels (GCP).

## Testing

- lint: `make lint`
- test: `make test`

## Inspiration

Inspired by [mtougeron/k8s-pvc-tagger](https://github.com/mtougeron/k8s-pvc-tagger) which provides similar functionality for copying PVC labels to the underlying cloud provider's disk.