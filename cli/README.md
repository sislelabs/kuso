# kuso-cli

![Version](https://img.shields.io/github/v/release/kuso-dev/kuso-cli)
![Build Status](https://img.shields.io/github/actions/workflow/status/kuso-dev/kuso-cli/build.yml?branch=main)
![License](https://img.shields.io/github/license/kuso-dev/kuso-cli)

A powerful and user-friendly Command Line Interface (CLI) for [Kuso](https://github.com/kuso-dev/kuso), the simplest Platform as a Service (PaaS) for Kubernetes.

> **Note:** Please report any issues in the [main repository](https://github.com/kuso-dev/kuso).

---

## Table of Contents

- [Features](#features)
- [Installation](#installation)
   - [Supported Platforms](#supported-platforms)
   - [1. Shortcut Installation](#1-shortcut-installation)
   - [2. Homebrew Installation](#2-homebrew-installation)
   - [3. Build from Source](#3-build-from-source)
- [Supported Providers](#supported-providers)
- [Usage](#usage)
   - [Command Overview](#command-overview)
- [Provider Credentials](#provider-credentials)
   - [Scaleway](#scaleway)
   - [Linode](#linode)
   - [DigitalOcean](#digitalocean)
   - [Google GKE](#google-gke)
- [Development Guide](#development-guide)
- [Contributing](#contributing)
- [License](#license)
- [Acknowledgments](#acknowledgments)

---

## Features

- **Easy Cluster Deployment:** Quickly create Kubernetes clusters on supported providers.
- **App Management:** Simplify application deployment and management.
- **Pipeline Integration:** Seamlessly integrate CI/CD pipelines.
- **User-Friendly Commands:** Intuitive CLI commands for efficient workflows.
- **Dashboard Access:** Easy access to the Kuso dashboard for monitoring.

---

## Installation

### Supported Platforms

- **macOS**
- **Linux**

### 1. Shortcut Installation

Install Kuso CLI with a single command:

```shell
curl -fsSL get.kuso.sislelabs.com | bash
```

### 2. Homebrew Installation

If Homebrew is not installed, install it first:

```shell
/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
```

Install Kuso CLI via Homebrew:

```shell
brew tap kuso-dev/kuso
brew install kuso-cli
```

### 3. Build from Source

For advanced use cases, build and package the binary manually.

#### Requirements

- [Git](https://git-scm.com/downloads)
- [Go](https://go.dev/doc/install)
- [UPX](https://github.com/upx/upx/releases/)

#### Steps

1. **Clone the Repository:**

   ```shell
   git clone https://github.com/kuso-dev/kuso-cli.git
   ```

2. **Navigate to the Project Directory:**

   ```shell
   cd kuso-cli
   ```

3. **Create a Version Tag (Optional):**

   ```shell
   git tag -a v1.0 -m "Version 1.0"
   ```

4. **Build and Package the Binary:**

   ```shell
   cd cmd
   go build -ldflags "-s -w -X main.version=$(git describe --tags --abbrev=0) -X main.commit=$(git rev-parse --short HEAD) -X main.date=$(date +%Y-%m-%d)" -trimpath -o kuso-cli
   upx kuso-cli
   mv kuso-cli ../kuso
   cd ..
   ```

5. **Move the Binary to Your PATH:**

   ```shell
   sudo mv kuso /usr/local/bin/
   ```

6. **Reload Shell Configuration:**

   ```shell
   source "$HOME/.$(basename ${SHELL})rc"
   ```

7. **Verify Installation:**

   ```shell
   kuso version
   ```

8. **Check for Updates:** (Optional)

   ```shell
   kuso version check
   ```

---

## Supported Providers

Kuso CLI currently supports the following cloud providers:

- **Scaleway**
- **Linode**
- **DigitalOcean**
- **Google GKE**
- **Kind** (local clusters)

### Coming Soon

- **Vultr**
- **Oracle Cloud OCI/OKE**
- **Exoscale**
- **Swissflow**

---

## Usage

### Command Overview

```plaintext
kuso
├── install                # Create a Kubernetes cluster and install Kuso with all required components
|                          # Can also be used to install Kuso on an existing cluster
├── login (li)             # Log in to Kuso and save credentials
├── logout (lo)            # Log out from Kuso and remove saved credentials
├── remote (r)             # List Kuso cluster
│   ├── create             # Create a cluster configuration
│   ├── delete             # Delete a cluster configuration
│   └── select             # Select a cluster
├── app (a)                # List Kuso apps
│   ├── create             # Create an app
│   └── delete             # Delete an app
├── pipeline (p)           # List Kuso pipelines
│   ├── create             # Create a pipeline
│   └── delete             # Delete a pipeline
├── config                 # View available configurations
│   ├── addons             # List addons
│   ├── runpacks           # List runpacks
│   └── podsizes           # List pod size configurations
├── dashboard (db)         # Open the Kuso dashboard
├── debug                  # Gather debug information
├── tunnel (t)             # Open a tunnel to a NAT-ed cluster
└── help                   # Display help for commands
```

### Usage with most common commands
Create a new cluster and install Kuso:

```shell
kuso install
```

Create a new app configuration:
```shell
kuso app create
```

Destroy an app:
```shell
kuso app delete
```

List all running pipelines:
```shell
kuso pipelines
```

Open the Kuso dashboard:

```shell
kuso dashboard
```

For more information, use the `--help` flag with any command:

```shell
kuso --help
```


---

## Provider Credentials

Set the appropriate environment variables for your cloud provider before using Kuso CLI.

### Scaleway

```shell
export SCALEWAY_ACCESS_TOKEN=your_access_token
export SCALEWAY_PROJECT_ID=your_project_id
export SCALEWAY_ORGANIZATION_ID=your_organization_id
```

### Linode

```shell
export LINODE_ACCESS_TOKEN=your_access_token
```

### DigitalOcean

```shell
export DIGITALOCEAN_ACCESS_TOKEN=your_access_token
```

### Google GKE

```shell
export GOOGLE_API_KEY=your_api_key
```

---

## Development Guide

### Enable Development Mode

To enable development mode for testing and debugging, create a `VERSION` file:

```shell
echo "dev" > cmd/kusoCli/VERSION
```

---

## Contributing

We welcome contributions from the community! Please check out our [Contributing Guidelines](https://github.com/kuso-dev/kuso/blob/main/CONTRIBUTING.md) for more information.

---

## License

This project is licensed under the [MIT License](LICENSE).

---

## Acknowledgments

- **[Kuso](https://github.com/kuso-dev/kuso):** The simplest PaaS for Kubernetes.
- **[Go](https://golang.org/):** The programming language used for development.
- **Community Contributors:** Thank you to all who have contributed to this project.

---

Thank you for using **kuso-cli**! If you have suggestions or encounter issues, please open an issue in the [main repository](https://github.com/kuso-dev/kuso).

---
