package main

// Wallet Key importer and relay miner config utility for Cosmos-SDK-based blockchains.
// Supports loading keys from Kubernetes Secrets/ConfigMaps or filesystem files.
// Also, populates relay miner configuration as required.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/go-bip39"
	"github.com/joho/godotenv"
	poktrollconfig "github.com/pokt-network/poktroll/pkg/relayer/config"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gopkg.in/yaml.v2"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// AppConfig centralizes all environment-driven settings.
type AppConfig struct {
	GenerateRelayMinerConfig bool
	AddressPrefix            string
	KeyringAppName           string
	KeyringBackend           string
	/*
	 * Directory for storing the keyring (default: shannon-keyring-loader)
	 * IMPORTANT: this will work only for test which will write to this path
	 * if this is relative, it will resolve the absolute, but better approach uses absolute here.
	 * IMPORTANT: this is ignored when using pass, because it will store the under `pass` folder `~/.password-store/keyring-pocket`
	 * NOTE: `os`, `file` `are` not tested.
	 */
	KeyringDir   string
	ConfigSource string

	KeysNamespace  string
	KeysSecretName string
	KeysSecretKey  string
	KeysFilePath   string

	RelayMinerConfigNamespace      string
	RelayMinerConfigName           string
	RelayMinerConfigKey            string
	RelayMinerConfigFilePath       string
	RelayMinerConfigFileOutputPath string
}

// WalletKeySpec represents the structure for key definition and import.
// One of Mnemonic OR Hex is required.
type WalletKeySpec struct {
	Mnemonic   string   `json:"mnemonic,omitempty"`
	StartIndex int      `json:"start_index,omitempty"`
	EndIndex   int      `json:"end_index,omitempty"`
	Hex        string   `json:"hex,omitempty"`
	ServiceID  []string `json:"service_id,omitempty"`
}

// Source types for config loader
const (
	KubernetesSource string = "kubernetes"
	FileSource       string = "file"
	ConfigMapSource  string = "configmap"
	SecretSource     string = "secret"
)

// getenv returns env value or fallback.
func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// loadEnv loads environment variables from a .env file if it exists in the current directory and returns an error if loading fails.
func loadEnv() error {
	if _, err := os.Stat(".env"); err == nil {
		if loadErr := godotenv.Load(".env"); loadErr != nil {
			return loadErr
		}
	}
	return nil
}

// configureLogger initializes global logging configuration based on environment variables and application config.
// It sets log level, console output format, and log colorization. Returns an error if log level parsing fails.
func configureLogger() error {
	// this will log the envs on his own because need to be set up before app config.
	level, err := zerolog.ParseLevel(getenv("LOG_LEVEL", "info"))
	if err != nil {
		return err
	}
	// Set the global log level
	zerolog.SetGlobalLevel(level)

	logColor := getenv("LOG_COLOR", "true") == "true"

	consoleWriter := zerolog.ConsoleWriter{
		Out:        os.Stderr,
		TimeFormat: time.RFC3339,
		NoColor:    !logColor,
	}

	log.Logger = log.With().Timestamp().Logger().Output(consoleWriter)

	return nil
}

// loadAppConfig loads and returns all configs from the environment (with defaults).
func loadAppConfig() *AppConfig {
	return &AppConfig{
		GenerateRelayMinerConfig: getenv("GENERATE_RELAYMINER_CONFIG", "true") == "true",
		AddressPrefix:            getenv("ADDRESS_PREFIX", "pokt"),

		KeyringAppName: getenv("KEYRING_APP_NAME", "pocket"),
		KeyringBackend: getenv("KEYRING_BACKEND", "test"),
		KeyringDir:     getenv("KEYRING_DIR", "shannon-keyring-loader"),

		ConfigSource: getenv("CONFIG_SOURCE", "file"),

		KeysNamespace:  getenv("KEYS_NAMESPACE", "default"),
		KeysSecretName: getenv("KEYS_SECRET_NAME", "pocket-keys"),
		KeysSecretKey:  getenv("KEYS_SECRET_KEY", "keys.json"),
		KeysFilePath:   getenv("KEYS_FILE_PATH", "keys.json"),

		RelayMinerConfigNamespace:      getenv("RELAYMINER_CONFIG_NAMESPACE", "default"),
		RelayMinerConfigName:           getenv("RELAYMINER_CONFIG_NAME", "pocket-relayminer-config"),
		RelayMinerConfigKey:            getenv("RELAYMINER_CONFIG_KEY", "config.yaml"),
		RelayMinerConfigFilePath:       getenv("RELAYMINER_CONFIG_FILE_PATH", "config.yaml"),
		RelayMinerConfigFileOutputPath: getenv("RELAYMINER_CONFIG_FILE_OUTPUT_PATH", "generated.config.yaml"),
	}
}

// validateConfig ensures that the provided AppConfig has valid settings for a keyring backend and configuration source.
// Returns an error if the keyring backend or config source is invalid.
func validateConfig(appConfig *AppConfig) error {
	log.Debug().Msg("Validating application configuration")

	// TBD(@jorgecuesta) should we validate the k8s resources or files here or leave it to fail on the read?
	if appConfig.KeyringBackend != "test" &&
		appConfig.KeyringBackend != "pass" &&
		appConfig.KeyringBackend != "os" {
		log.Error().Str("backend", appConfig.KeyringBackend).Msg("Unsupported keyring backend")
		return fmt.Errorf("unsupported keyring backend: %s", appConfig.KeyringBackend)
	}

	if appConfig.ConfigSource != KubernetesSource && appConfig.ConfigSource != FileSource {
		log.Error().Str("source", appConfig.ConfigSource).Msg("Invalid config source")
		return fmt.Errorf("invalid config source: %s", appConfig.ConfigSource)
	}

	if !filepath.IsAbs(appConfig.KeyringDir) {
		absPath, err := filepath.Abs(appConfig.KeyringDir)
		if err != nil {
			return fmt.Errorf("failed to convert to absolute path: %w", err)
		}
		appConfig.KeyringDir = absPath
	}

	log.Debug().Msg("Configuration validation successful")
	return nil
}

// getCodec initializes protobuf codec with cosmos crypto interfaces.
func getCodec() codec.Codec {
	// Create a new interface registry
	interfaceRegistry := types.NewInterfaceRegistry()

	// Create a new ProtoCodec
	marshaler := codec.NewProtoCodec(interfaceRegistry)

	// Register crypto interfaces
	cryptocodec.RegisterInterfaces(interfaceRegistry)

	return marshaler
}

// configureSdk sets Cosmos bech32 prefixes (account, validator, consensus).
func configureSdk(appConfig *AppConfig) {
	log.Debug().Msg("Configuring Cosmos SDK")

	// Get address prefix from env var, default to "pokt"
	prefix := appConfig.AddressPrefix

	log.Info().Str("address_prefix", prefix).Msg("Setting Cosmos SDK address prefix")

	// Configure SDK to use the specified prefix
	config := sdk.GetConfig()
	config.SetBech32PrefixForAccount(prefix, prefix+"pub")
	config.SetBech32PrefixForValidator(prefix+"valoper", prefix+"valoperpub")
	config.SetBech32PrefixForConsensusNode(prefix+"valcons", prefix+"valconspub")
	config.Seal()

	log.Debug().Msg("Cosmos SDK configuration completed")
}

// derivePrivateKeyFromMnemonic derives a secp256k1 key from a mnemonic and index.
func derivePrivateKeyFromMnemonic(mnemonic string, index uint32) (*secp256k1.PrivKey, error) {
	// Convert mnemonic to seed
	seed := bip39.NewSeed(mnemonic, "") // Empty password for seed generation

	// Define the HD path. For the Cosmos, it's typically "m/44'/118'/0'/0/index"
	hdPath := hd.NewFundraiserParams(0, sdk.CoinType, index).String()

	// Derive the private key using the seed and path
	masterPriv, ch := hd.ComputeMastersFromSeed(seed)
	derivedPriv, err := hd.DerivePrivateKeyForPath(masterPriv, ch, hdPath)
	if err != nil {
		return nil, err
	}

	// Create a new private key from the derived bytes
	privKey := &secp256k1.PrivKey{Key: derivedPriv}

	return privKey, nil
}

// newKeyring initializes and returns a keyring instance based on environment variables and a codec.
func newKeyring(appConfig *AppConfig) (keyring.Keyring, error) {
	log.Debug().Msg("Initializing keyring")

	// Get the codec
	cdc := getCodec()

	log.Info().
		Str("app_name", appConfig.KeyringAppName).
		Str("backend", appConfig.KeyringBackend).
		Str("dir", appConfig.KeyringDir).
		Msg("Creating new keyring")

	// Initialize Cosmos SDK keyring
	kr, err := keyring.New(
		appConfig.KeyringAppName,
		appConfig.KeyringBackend,
		appConfig.KeyringDir,
		nil,
		cdc,
	)
	if err != nil {
		log.Error().Err(err).Msg("Failed to initialize keyring")
		return nil, fmt.Errorf("error initializing keyring: %w", err)
	}

	log.Debug().Msg("Keyring initialized successfully")
	return kr, nil
}

// importSecp256k1PrivateKey handles the common logic for importing a private key into the keyring
func importSecp256k1PrivateKey(kr keyring.Keyring, privKey *secp256k1.PrivKey) (string, error) {
	address := sdk.AccAddress(privKey.PubKey().Address())
	name := address.String()

	log.Debug().Str("address", address.String()).Msg("Attempting to import private key")

	if acc, err := kr.KeyByAddress(address); err == nil {
		if acc.Name != name {
			log.Warn().
				Str("existing_name", acc.Name).
				Str("calculated_name", name).
				Msg("Key already exists with a different name")
		} else {
			log.Debug().Str("name", name).Msg("Key already exists in keyring")
		}
		// respect the name of the key if it's different from the address,
		// who knows why the user set it
		// allowing this we maybe help this tool be used for dev/test environments?
		return acc.Name, nil
	} else if !strings.Contains(err.Error(), "not found") {
		// not found is ok - anything else is not
		log.Error().Err(err).Str("address", address.String()).Msg("Error checking key existence")
		return "", err
	}

	log.Debug().Str("name", name).Msg("Key not found in keyring, importing")

	// the address isn't found, so let's import it
	err := kr.ImportPrivKeyHex(name, hex.EncodeToString(privKey.Key), "secp256k1")
	if err != nil {
		log.Error().Err(err).Str("name", name).Msg("Failed to import private key")
		return "", err
	}

	log.Info().Str("name", name).Msg("Successfully imported key")
	return name, nil
}

// readFile reads the contents of the file specified by filePath and returns it as a byte slice or an error if unsuccessful.
func readFile(filePath string) ([]byte, error) {
	log.Debug().Str("path", filePath).Msg("Reading file")
	data, err := os.ReadFile(filePath)
	if err != nil {
		log.Error().Err(err).Str("path", filePath).Msg("Failed to read file")
	} else {
		log.Debug().Str("path", filePath).Int("bytes_read", len(data)).Msg("File read successfully")
	}
	return data, err
}

// loadConfigData loads configuration data from either a file, ConfigMap, or Secret, based on the specified source.
// `source` determines whether to use a ConfigMap or Secret as the configuration source.
// `namespace` is the Kubernetes namespace where the ConfigMap or Secret is located.
// `name` is the name of the ConfigMap or Secret in Kubernetes.
// `key` specifies the key within the ConfigMap or Secret data to retrieve.
// `configPath` specifies the file path for a local file configuration.
// Returns the configuration data as a byte slice or an error if retrieval fails.
func loadConfigData(appConfig *AppConfig, source, namespace, name, key, configPath string) ([]byte, error) {
	log.Debug().
		Str("config_source", appConfig.ConfigSource).
		Str("source", source).
		Str("namespace", namespace).
		Str("name", name).
		Str("key", key).
		Str("config_path", configPath).
		Msg("Loading data")

	// Get the configuration based on the source
	switch appConfig.ConfigSource {
	case KubernetesSource:
		// Initialize Kubernetes client
		config, err := rest.InClusterConfig()
		if err != nil {
			log.Error().Err(err).Msg("Failed to create in-cluster config")
			return nil, fmt.Errorf("error creating in-cluster config: %w", err)
		}

		clientset, err := kubernetes.NewForConfig(config)
		if err != nil {
			log.Error().Err(err).Msg("Failed to create Kubernetes clientset")
			return nil, fmt.Errorf("error creating Kubernetes clientset: %w", err)
		}

		// Fetch the file from Kubernetes
		var data []byte

		if source == ConfigMapSource {
			log.Info().
				Str("namespace", namespace).
				Str("name", name).
				Str("key", key).
				Msg("Loading from ConfigMap")

			configmap, err := clientset.CoreV1().ConfigMaps(namespace).Get(context.Background(), name, v1.GetOptions{})
			if err != nil {
				log.Error().Err(err).Str("namespace", namespace).Str("name", name).Msg("Failed to fetch ConfigMap")
				return nil, fmt.Errorf("error fetching configmap '%s' in namespace '%s': %w", name, namespace, err)
			}
			_data, ok := configmap.Data[key]
			if !ok {
				log.Error().Str("name", name).Str("key", key).Msg("ConfigMap does not contain key")
				return nil, fmt.Errorf("error: ConfigMap '%s' does not contain key '%s'", name, key)
			}

			data = []byte(_data)
			log.Debug().Msg("ConfigMap data loaded successfully")
		} else if source == SecretSource {
			log.Info().
				Str("namespace", namespace).
				Str("name", name).
				Str("key", key).
				Msg("Loading from Secret")

			secret, err := clientset.CoreV1().Secrets(namespace).Get(context.Background(), name, v1.GetOptions{})
			if err != nil {
				log.Error().Err(err).Str("namespace", namespace).Str("name", name).Msg("Failed to fetch Secret")
				return nil, fmt.Errorf("error fetching secret '%s' in namespace '%s': %w", name, namespace, err)
			}

			// Extract JSON data from the secret
			_data, ok := secret.Data[key]
			if !ok {
				log.Error().Str("name", name).Str("key", key).Msg("Secret does not contain key")
				return nil, fmt.Errorf("error: Secret '%s' does not contain key '%s'", name, key)
			}

			data = _data
			log.Debug().Msg("Secret data loaded successfully")
		} else {
			log.Error().Str("source", source).Msg("Unsupported Kubernetes resource type")
			return nil, fmt.Errorf("unsupported configuration source: %s", source)
		}

		return data, nil
	case FileSource:
		log.Info().Str("path", configPath).Msg("Loading configuration from file")
		data, err := readFile(configPath)
		if err != nil {
			log.Error().Err(err).Str("path", configPath).Msg("Failed to read file")
		} else {
			log.Debug().Msg("File data loaded successfully")
		}
		return data, err
	default:
		log.Error().Str("source", appConfig.ConfigSource).Msg("Unsupported configuration source")
		return nil, fmt.Errorf("unsupported configuration source: %s", appConfig.ConfigSource)
	}
}

// loadWalletKeys loads a list of wallet keys from a file or Kubernetes secret, based on the configured source.
// It retrieves and unmarshals wallet key specifications into a slice of WalletKeySpec structs for further processing.
func loadWalletKeys(appConfig *AppConfig) ([]WalletKeySpec, error) {
	keys := make([]WalletKeySpec, 0)

	// Extract JSON file from the secret
	jsonData, err := loadConfigData(
		appConfig,
		SecretSource,
		appConfig.KeysNamespace,
		appConfig.KeysSecretName,
		appConfig.KeysSecretKey,
		appConfig.KeysFilePath,
	)
	if err != nil {
		log.Error().Err(err).Msg("Failed to load wallet keys configuration")
		return keys, fmt.Errorf("error loading configuration: %w", err)
	}

	// Parse JSON data
	log.Debug().Int("data_size", len(jsonData)).Msg("Parsing wallet keys JSON data")
	if err := json.Unmarshal(jsonData, &keys); err != nil {
		log.Error().Err(err).Msg("Failed to parse wallet keys JSON data")
		return keys, fmt.Errorf("error parsing JSON data from secret: %w", err)
	}

	log.Info().Int("key_count", len(keys)).Msg("Wallet keys loaded successfully")
	return keys, nil
}

// loadRelayMinerConfig loads the Relay Miner configuration from a file or Kubernetes ConfigMap.
// It retrieves and unmarshals the configuration into a YAMLRelayMinerConfig object.
// Returns the unmarshaled configuration or logs a fatal error if loading fails.
func loadRelayMinerConfig(appConfig *AppConfig) (*poktrollconfig.YAMLRelayMinerConfig, error) {
	log.Info().Msg("Loading relay miner configuration")
	yamlRelayMinerConfig := &poktrollconfig.YAMLRelayMinerConfig{}

	if !appConfig.GenerateRelayMinerConfig {
		log.Debug().Msg("Skipping relay miner config generation as it is disabled")
		return nil, nil
	}

	// Extract a config file from the source
	log.Debug().
		Str("namespace", appConfig.RelayMinerConfigNamespace).
		Str("config_name", appConfig.RelayMinerConfigName).
		Str("config_key", appConfig.RelayMinerConfigKey).
		Str("file_path", appConfig.RelayMinerConfigFilePath).
		Msg("Loading relay miner configuration data")

	configContent, err := loadConfigData(
		appConfig,
		ConfigMapSource,
		appConfig.RelayMinerConfigNamespace,
		appConfig.RelayMinerConfigName,
		appConfig.RelayMinerConfigKey,
		appConfig.RelayMinerConfigFilePath,
	)
	if err != nil {
		log.Error().Err(err).Msg("Failed to load relay miner configuration")
		return nil, fmt.Errorf("error loading configuration: %w", err)
	}

	// Unmarshal the config file into a yamlRelayMinerConfig
	log.Debug().Int("content_size", len(configContent)).Msg("Parsing relay miner YAML configuration")
	err = yaml.Unmarshal(configContent, yamlRelayMinerConfig)
	if err != nil {
		log.Error().Err(err).Msg("Failed to unmarshal relay miner YAML configuration")
		return nil, fmt.Errorf("unable to unmarshall RelayMiner config file: %w", err)
	}

	log.Info().Msg("Relay miner configuration loaded successfully")
	return yamlRelayMinerConfig, nil
}

// importAndRegisterKeys imports wallet keys into the keyring and registers them in the relay miner configuration.
func importAndRegisterKeys(appConfig *AppConfig, keys []WalletKeySpec, walletKeyring keyring.Keyring, relayMinerConfig *poktrollconfig.YAMLRelayMinerConfig) error {
	log.Info().
		Int("keys", len(keys)).
		Msg("Importing and registering keys")

	name := ""

	for i, entry := range keys {
		if entry.Mnemonic != "" {
			// Process mnemonic
			if !bip39.IsMnemonicValid(entry.Mnemonic) {
				return fmt.Errorf("invalid mnemonic at index: %d", i)
			}

			for j := entry.StartIndex; j <= entry.EndIndex; j++ {
				privKey, err := derivePrivateKeyFromMnemonic(entry.Mnemonic, uint32(j))
				if err != nil {
					return fmt.Errorf("error deriving private key at index %d: %w", j, err)
				}

				name, err = importSecp256k1PrivateKey(walletKeyring, privKey)
				if err != nil {
					return fmt.Errorf("error importing derived key at index %d: %w", j, err)
				}

				if entry.ServiceID == nil || len(entry.ServiceID) == 0 {
					err = registerRelayMinerConfig(appConfig, name, "", relayMinerConfig)
					if err != nil {
						return err
					}
				} else {
					for _, serviceId := range entry.ServiceID {
						err = registerRelayMinerConfig(appConfig, name, serviceId, relayMinerConfig)
						if err != nil {
							return err
						}
					}
				}
			}
		} else if entry.Hex != "" {
			// Process hex private key
			privKeyHex := strings.TrimPrefix(entry.Hex, "0x")
			privKeyBytes, err := hex.DecodeString(privKeyHex)
			if err != nil {
				return fmt.Errorf("error decoding hex key: %w", err)
			}

			privKey := &secp256k1.PrivKey{Key: privKeyBytes}
			name, err = importSecp256k1PrivateKey(walletKeyring, privKey)
			if err != nil {
				return fmt.Errorf("error importing hex key: %w", err)
			}

			if entry.ServiceID == nil || len(entry.ServiceID) == 0 {
				err = registerRelayMinerConfig(appConfig, name, "", relayMinerConfig)
				if err != nil {
					return err
				}
			} else {
				for _, serviceId := range entry.ServiceID {
					err = registerRelayMinerConfig(appConfig, name, serviceId, relayMinerConfig)
					if err != nil {
						return err
					}
				}
			}
		} else {
			return fmt.Errorf("invalid entry index: %d", i)
		}
	}

	return nil
}

// writeRelayMinerConfig updates a Relay Miner configuration file with the provided YAMLRelayMinerConfig object.
// Reads environment variables for input/output paths and writes the updated file, retaining original permissions.
// Log fatal errors if file operations or YAML marshaling fails.
func writeRelayMinerConfig(appConfig *AppConfig, relayMinerConfig *poktrollconfig.YAMLRelayMinerConfig) error {
	var mode os.FileMode = 0644

	// ignore generating relayminer config when GENERATE_RELAYMINER_CONFIG=false 
	if !appConfig.GenerateRelayMinerConfig {
		log.Debug().Msg("Skipping relay miner config generation as it is disabled")
		return nil
	}
	
	// only if we read the file from the disk, we can keep the original permissions
	if appConfig.ConfigSource == FileSource {
		// Get file info for original permissions
		fileInfo, err := os.Stat(appConfig.RelayMinerConfigFilePath)
		if err != nil {
			return fmt.Errorf("unable to get config file info: %w", err)
		}

		mode = fileInfo.Mode()
	}

	// Marshal the updated config back to YAML
	updatedContent, err := yaml.Marshal(relayMinerConfig)
	if err != nil {
		return fmt.Errorf("unable to marshal updated config: %w", err)
	}

	// Write the updated content to the output file (input could be read-only in some environments)
	err = os.WriteFile(appConfig.RelayMinerConfigFileOutputPath, updatedContent, mode)
	if err != nil {
		return fmt.Errorf("unable to write updated config file: %w", err)
	}

	log.Info().
		Str("path", appConfig.RelayMinerConfigFileOutputPath).
		Msg("Relay miner configuration file updated successfully")

	return nil
}

// registerRelayMinerConfig updates the relay miner configuration with a signing key name for a service ID or default.
// If serviceId is provided, it adds the key name to the corresponding supplier. Otherwise, it updates the default list.
// The function exits early if GenerateRelayMinerConfig is false or if the service ID is not found among suppliers.
func registerRelayMinerConfig(appConfig *AppConfig, name, serviceId string, relayMinerConfig *poktrollconfig.YAMLRelayMinerConfig) error {
	if !appConfig.GenerateRelayMinerConfig {
		return nil
	}

	log.Debug().
		Str("name", name).
		Str("service_id", serviceId).
		Msg("Registering wallet to relayminer config")
	// if service id, add to service id signing key names
	if serviceId != "" {
		found := false
		for j := range relayMinerConfig.Suppliers {
			supplierConfig := &relayMinerConfig.Suppliers[j]
			if supplierConfig.ServiceId == serviceId {
				if supplierConfig.SigningKeyNames == nil {
					supplierConfig.SigningKeyNames = []string{}
				}
				supplierConfig.SigningKeyNames = append(supplierConfig.SigningKeyNames, name)
				found = true // mark if at least one service id is found.
			}
		}

		if !found {
			return fmt.Errorf("service id not found under suppliers[].service_id: %s", serviceId)
		}
	} else {
		// if not service id, add to default signing key names
		if relayMinerConfig.DefaultSigningKeyNames == nil {
			relayMinerConfig.DefaultSigningKeyNames = []string{}
		}
		relayMinerConfig.DefaultSigningKeyNames = append(relayMinerConfig.DefaultSigningKeyNames, name)
	}

	return nil
}

func main() {
	var walletKeyring keyring.Keyring
	var relayMinerConfig *poktrollconfig.YAMLRelayMinerConfig
	var keys []WalletKeySpec
	var err error

	err = loadEnv()
	if err != nil {
		log.Fatal().Err(err)
	}

	err = configureLogger()
	if err != nil {
		log.Fatal().Err(err)
	}

	appConfig := loadAppConfig()

	err = validateConfig(appConfig)
	if err != nil {
		log.Fatal().Err(err).Msg("error validating config")
	}

	// Configure the sdk to use the right account prefix
	configureSdk(appConfig)

	// Read keys from a local file or kubernetes secret depending on CONFIG_SOURCE
	keys, err = loadWalletKeys(appConfig)
	if err != nil {
		log.Fatal().Err(err).Msg("error loading wallet keys")
	}

	// Initialize cosmos walletKeyring
	walletKeyring, err = newKeyring(appConfig)
	if err != nil {
		log.Fatal().Err(err).Msg("error initializing keyring")
	}

	// Read relay miner config (will be nil if GenerateRelayMinerConfig is false)
	relayMinerConfig, err = loadRelayMinerConfig(appConfig)
	if err != nil {
		log.Fatal().Err(err).Msg("error loading relay miner config")
	}

	// Process keys
	err = importAndRegisterKeys(appConfig, keys, walletKeyring, relayMinerConfig)
	if err != nil {
		log.Fatal().Err(err).Msg("error processing keys")
	}

	// Update relay miner config
	err = writeRelayMinerConfig(appConfig, relayMinerConfig)
	if err != nil {
		log.Fatal().Err(err).Msg("error writing relay miner config")
	}

	log.Info().Msg("All keys processed successfully.")
}
