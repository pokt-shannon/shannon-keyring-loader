# Cosmos-SDK Key Importer & Relay Miner Config Utility

This utility imports wallet keys into a Cosmos SDK-compatible keyring (from mnemonics or raw hex private keys) and optionally updates a relay miner configuration file with those keys. It can load configurations from the filesystem or Kubernetes (Secrets/ConfigMaps).

## Table of Contents
1. [Environment Variables](#environment-variables)
2. [Usage](#usage)
  - [Running Locally](#running-locally)
  - [Running via Docker](#running-via-docker)
3. [Configuration Sources](#configuration-sources)

---

## Environment Variables

Below are the primary environment variables recognized by this utility, along with their defaults in parentheses:

| Variable                               | Description                                                                                                                                                        | Default                     |
|----------------------------------------|--------------------------------------------------------------------------------------------------------------------------------------------------------------------|-----------------------------|
| **LOG_LEVEL**                          | Define log lever                                                                                                                                                   | `info`                      |
| **LOG_COLOR**                          | If set to `"true"`, turn on log colors. Anything that is not `true` results in falsy.                                                                              | `true`                      |
| **GENERATE_RELAYMINER_CONFIG**         | If set to `"true"`, the tool updates the Relay Miner config with key information. Otherwise, it simply imports keys. Anything that is not `true` results in falsy. | `true`                      |
| **ADDRESS_PREFIX**                     | Bech32 address prefix to use for Cosmos SDK addresses.                                                                                                             | `pokt`                      |
| **KEYRING_APP_NAME**                   | The Cosmos SDK keyring application name.                                                                                                                           | `pocket`                    |
| **KEYRING_BACKEND**                    | The Cosmos SDK keyring backend (e.g., `test`, `file`, `pass`, `os`).                                                                                               | `test`                      |
| **KEYRING_DIR**                        | Directory path where the keyring is stored (note that certain backends like `pass` or `os` might override this).                                                   | `shannon-keyring-loader`    |
| **CONFIG_SOURCE**                      | Controls how config/scopes are loaded. Accepts `file` or `kubernetes`.                                                                                             | `file`                      |
| **KEYS_NAMESPACE**                     | If `CONFIG_SOURCE=kubernetes`, specifies the namespace containing the Secret with keys.                                                                            | `default`                   |
| **KEYS_SECRET_NAME**                   | If `CONFIG_SOURCE=kubernetes`, the name of the Secret that holds your keys.                                                                                        | `pocket-keys`               |
| **KEYS_SECRET_KEY**                    | If `CONFIG_SOURCE=kubernetes`, the key within the Secret that holds the JSON array of key specs.                                                                   | `keys.json`                 |
| **KEYS_FILE_PATH**                     | If `CONFIG_SOURCE=file`, path to the JSON file describing keys.                                                                                                    | `keys.json`                 |
| **RELAYMINER_CONFIG_NAMESPACE**        | If `CONFIG_SOURCE=kubernetes`, the namespace for the Relay Miner ConfigMap or Secret.                                                                              | `default`                   |
| **RELAYMINER_CONFIG_NAME**             | If `CONFIG_SOURCE=kubernetes`, the name of the Relay Miner ConfigMap or Secret.                                                                                    | `pocket-relayminer-config`  |
| **RELAYMINER_CONFIG_KEY**              | If `CONFIG_SOURCE=kubernetes`, the data key within the Relay Miner ConfigMap or Secret that holds the YAML config.                                                 | `config.yaml`               |
| **RELAYMINER_CONFIG_FILE_PATH**        | If `CONFIG_SOURCE=file`, path to the local Relay Miner YAML config file.                                                                                           | `config.yaml`               |
| **RELAYMINER_CONFIG_FILE_OUTPUT_PATH** | Output path for the updated Relay Miner YAML config after keys are imported.                                                                                       | `generated.config.yaml`     |

---

## Usage

### Running Locally

1. Ensure you have Go (≥1.23) installed.
2. Download or clone this repository.
3. Set up your environment variables as needed (you may use a `.env` file, export them in your shell, or specify them inline).
4. Build and run:

   ```bash
   go build -o keyimporter
   ./keyimporter
   ```

  - The program reads all listed environment variables and performs key import.
  - If `GENERATE_RELAYMINER_CONFIG=true`, it updates your Relay Miner config file or resource.

Example (file-based sources):

  ```bash
  export CONFIG_SOURCE=file 
  export KEYS_FILE_PATH=./keys.json 
  export RELAYMINER_CONFIG_FILE_PATH=./config.yaml
  ./keyimporter
  ```

This will read keys from `keys.json` and update `config.yaml`, saving changes to `generated.config.yaml` by default.

### Running via Docker

1. Build your Docker image (or use an existing one that runs this utility).
2. Place your key specification file and config file where you can mount them into the container at runtime.
3. Run something like:

   ```bash
   docker run --rm \
     -v "$(pwd)/keys.json:/app/keys.json:ro" \
     -v "$(pwd)/config.yaml:/app/config.yaml:ro" \
     -v "$(pwd)/generated:/app/generated" \
     -e KEYRING_DIR=/app/generated/keyring-test \
     -e CONFIG_SOURCE=file \
     -e KEYS_FILE_PATH=/app/keys.json \
     -e RELAYMINER_CONFIG_FILE_PATH=/app/config.yaml \
     -e RELAYMINER_CONFIG_FILE_OUTPUT_PATH=/app/generated/config.yaml \
     <your-docker-image>
   ```

   This:
  - Mounts local `keys.json` and `config.yaml` in read-only mode into the container.
  - Mounts `generated` folder as volume to get the generated keyring and config.yaml
  - Tells the utility to load from `/app/keys.json` and `/app/config.yaml`.
  - Writes updated config to `/app/generated.config.yaml`.

To switch to Kubernetes-based sources, set `CONFIG_SOURCE=kubernetes` and the appropriate `KEYS_NAMESPACE`, `KEYS_SECRET_NAME`, etc. The container must then run in a cluster environment to access the in-cluster configuration.

---

## Configuration Sources

- **File-based**: Use `CONFIG_SOURCE=file` and specify `KEYS_FILE_PATH` for your JSON file. If generating a relay miner config, also specify `RELAYMINER_CONFIG_FILE_PATH` and `RELAYMINER_CONFIG_FILE_OUTPUT_PATH`.
- **Kubernetes-based**: Use `CONFIG_SOURCE=kubernetes` and provide details for `KEYS_NAMESPACE`, `KEYS_SECRET_NAME`, `KEYS_SECRET_KEY`, as well as `RELAYMINER_CONFIG_NAMESPACE`, `RELAYMINER_CONFIG_NAME`, and `RELAYMINER_CONFIG_KEY`. The utility will read these from in-cluster Kubernetes Secrets/ConfigMaps.

---

**Enjoy importing and managing your Cosmos-SDK keys with ease!**
