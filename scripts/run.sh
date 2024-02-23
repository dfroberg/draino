#!/bin/bash
./draino --kubeconfig ~/.kube/config-test-dev  --node-label-expr="metadata['labels']['node-role'] in ['default', 'default', 'default-compute', 'default-memory']" --evict-unreplicated-pods --evict-emptydir-pods --evict-daemonset-pods FrequentAwsNodeRestart KernelDeadlock ReadonlyFilesystem OutOfDisk 