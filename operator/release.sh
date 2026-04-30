#!/bin/bash
export VERSION=$(cat VERSION)
export IMG=ghcr.io/sislelabs/kuso-operator/kusoapp:v$VERSION
export BUNDLE_IMG=ghcr.io/sislelabs/kuso-operator/kusoapp-bundle:v$VERSION
make bundle
./bin/kustomize build config/default > deploy/operator.yaml
./bin/kustomize build config/default > deploy/operator.$VERSION.yaml


sed -i "" "s/VERSION ?= .*/VERSION ?= ${VERSION}/" Makefile
sed -i "" "s/    containerImage: ghcr.io\/sislelabs\/kuso-operator\/kusoapp:v.*/    containerImage: ghcr.io\/sislelabs\/kuso-operator\/kusoapp:v${VERSION}/" config/manifests/bases/kuso-operator.clusterserviceversion.yaml
sed -i "" "s/    containerImage: ghcr.io\/sislelabs\/kuso-operator\/kusoapp:v.*/    containerImage: ghcr.io\/sislelabs\/kuso-operator\/kusoapp:v${VERSION}/" bundle/manifests/kuso-operator.clusterserviceversion.yaml

#git tag v$(cat VERSION) --force && git push --tags --force