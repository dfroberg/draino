#!/bin/bash
./draino --debug \
    --kubeconfig $KUBECONFIG \
    --node-label-expr="$DAINO_NODE_LABEL_EXPR" \
    --max-grace-period=1m0s \
    --drain-buffer=5m0s \
    --eviction-headroom=30s \
    --evict-unreplicated-pods \
    --evict-emptydir-pods \
    --evict-daemonset-pods \
    FrequentAwsNodeRestart KernelDeadlock ReadonlyFilesystem OutOfDisk 