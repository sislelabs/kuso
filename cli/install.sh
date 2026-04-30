#!/usr/bin/env bash

################################################################################
# This Script is used to install kuso-cli binaries.                          #
#                                                                              #
# Supported OS: Linux, macOS ---> Windows(not supported)                       #
# Supported Architecture: amd64, arm64                                         #
# Source: https://github.com/kuso-dev/kuso-cli                             #
# Binary Release: https://github.com/kuso-dev/kuso-cli/releases/latest     #
# License: Apache License 2.0                                                  #
# Usage:                                                                       #
#   curl -fsSL get.kuso.sislelabs.com | bash                                           #
#   curl -fsSL get.kuso.sislelabs.com | bash -s -- v1.10.0                             #
#   bash <(curl -fsSL get.kuso.sislelabs.com) v1.9.2                                   #
################################################################################

set -eo pipefail
[[ $TRACE ]] && set -x

get_os() {
    case "$(uname -s)" in
        Linux*) echo "linux" ;;
        Darwin*) echo "darwin" ;;
        *) echo "unsupported" ;;
    esac
}

get_arch() {
    case "$(uname -m)" in
        x86_64) echo "amd64" ;;
        arm*|aarch64) echo "arm64" ;;
        *) echo "unsupported" ;;
    esac
}

os=$(get_os)
arch=$(get_arch)
version=${1:-latest}

if [[ "$os" == "unsupported" || "$arch" == "unsupported" ]]; then
    echo "Unsupported OS or architecture."
    exit 1
fi

if [[ -f "/usr/local/bin/kuso" ]]; then
    read -r -p "Do you want to replace it? [y/n] " replaceBinary
    [[ "$replaceBinary" != "y" && "$replaceBinary" != "" ]] && echo "Aborting installation." && exit 1
fi

release_url="https://github.com/kuso-dev/kuso-cli/releases/${version}/download/kuso-cli_${os}_${arch}.tar.gz"
temp_dir=$(mktemp -d)

echo "Downloading ${release_url} ..."
curl -L -s -o "${temp_dir}/kuso-cli.tar.gz" "$release_url" || { echo "Failed to download the binary."; rm -rf "$temp_dir"; exit 1; }

echo "Unpacking the binary..."
tar -xzvf "${temp_dir}/kuso-cli.tar.gz" -C "$temp_dir" || { echo "Failed to unpack the binary."; rm -rf "$temp_dir"; exit 1; }

[[ ! -f "${temp_dir}/kuso" ]] && { echo "Failed to unpack the binary."; rm -rf "$temp_dir"; exit 1; }

echo "Installing kuso in /usr/local/bin ..."
sudo mv "${temp_dir}/kuso" "/usr/local/bin/kuso" || { echo "Failed to install kuso."; rm -rf "$temp_dir"; exit 1; }

rm -rf "$temp_dir"
echo "Kuso has been successfully installed."
echo "Run 'kuso install' to create a kubernetes cluster."