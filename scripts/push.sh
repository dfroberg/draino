#!/usr/bin/env bash

set -e

VERSION=$(git rev-parse --short HEAD)
docker push "dfroberg/draino:${VERSION}"
