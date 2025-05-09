# k8s-node-tagger

A Kubernetes controller that watches Kubernetes Nodes and copies labels from the node to the cloud provider's VM as tags (AWS) or labels (GCP).

## Deployment

See the [./examples](./examples) directory for example manifests. These are just examples, please read them carefully and adjust if needed.

## Command-line Flags

- `-cloud`: Cloud provider to use (`aws` or `gcp`). Required.
- `-labels`: Comma-separated list of node label keys to sync to cloud provider tags.
- `-annotations`: Comma-separated list of node annotation keys to sync to cloud provider tags.
- `-json`: Output logs in JSON format.
- `-enable-leader-election`: Enable leader election for controller manager.
- `-metrics-addr`: The address the metric endpoint binds to (default ":8081").
- `-probes-addr`: The address the /readyz and /healthz probes endpoint binds to (default ":8080").

Either `-labels` or `-annotations` must be specified.

## Testing

- lint: `make lint`
- test: `make test`

Running it locally is possible too. The standard mechanisms for finding the current kubeconfig and aws/gcp credentials are used.

Assuming you use `~/.aws/config` and have profiles defined you could run:

```console
AWS_PROFILE=my-profile AWS_REGION=region go run -v .
```

For GCP you want to ensure you have application default credentials setup by running either `gcloud auth login --update-adc` or `gcloud auth application-default login`.

## Releasing

Releases are generated automatically on all successful `main` branch builds. This project uses [autotag](https://github.com/pantheon-systems/autotag) to automate this process.

Semver (`vMajor.Minor.Patch`) is used for versioning and releases. By default, `autotag` will bump the patch version on a successful main build, eg: `v1.0.0` -> `v1.0.1`.

To bump the major or minor release instead, include `[major]` or `[minor]` in the commit message. Refer to the autotag [docs](https://github.com/pantheon-systems/autotag#incrementing-major-and-minor-versions) for more details.

## Inspiration

Inspired by [mtougeron/k8s-pvc-tagger](https://github.com/mtougeron/k8s-pvc-tagger) which provides similar functionality for copying PVC labels to the underlying cloud provider's disk.
