# Build the manager binary
FROM quay.io/operator-framework/helm-operator:v1.26

# Annotate the image so GitHub auto-links the published package back to
# the source repo on the repository sidebar. Without these the package
# shows up under the org-wide /packages page only.
LABEL org.opencontainers.image.source="https://github.com/sislelabs/kuso"
LABEL org.opencontainers.image.description="kuso operator — helm-operator with the KusoProject/Service/Environment/Addon/Build CRDs"
LABEL org.opencontainers.image.licenses="GPL-3.0"

ENV HOME=/opt/helm
COPY watches.yaml ${HOME}/watches.yaml
COPY helm-charts  ${HOME}/helm-charts
WORKDIR ${HOME}
