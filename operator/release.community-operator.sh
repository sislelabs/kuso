#!/bin/bash
export VERSION=$(cat VERSION)

echo "Version: $VERSION"

cd ../community-operators

ls -altr ./operators/kuso-operator
echo "Enter the old version of the operator (e.g. 0.0.1):"
read OLD_VERSION
git co main
git pull
git branch add-upgrade-kuso-$VERSION
git co add-upgrade-kuso-$VERSION

cp -r ../kuso-operator/bundle ./operators/kuso-operator/$VERSION
echo "  replaces: kuso-operator.v$OLD_VERSION" >> ./operators/kuso-operator/$VERSION/manifests/kuso-operator.clusterserviceversion.yaml

git add .
git commit -s -m "operator kuso-operator ($VERSION)"
git push --set-upstream origin add-upgrade-kuso-$VERSION