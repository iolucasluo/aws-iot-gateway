// Package devicebootstrap owns the provider-neutral device service bootstrap contract.
package devicebootstrap

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	SchemaVersion = 1
	contractFile  = "contract.json"
)

var keyIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type Input struct {
	Environment      string
	ServiceBaseURL   string
	ServiceCAPEM     []byte
	TimeBootstrapURL string
	SignerBackend    string
	Keys             []KeyInput
}

type KeyInput struct {
	Kid     string
	SPKIDER []byte
}

type Contract struct {
	SchemaVersion int           `json:"schema_version"`
	Kind          string        `json:"kind"`
	Environment   string        `json:"environment"`
	Service       Service       `json:"service"`
	TimeBootstrap TimeBootstrap `json:"time_bootstrap"`
}

type Service struct {
	BaseURL     string `json:"base_url"`
	CAPEMFile   string `json:"ca_pem_file,omitempty"`
	CAPEMSHA256 string `json:"ca_pem_sha256,omitempty"`
}

type TimeBootstrap struct {
	URL  string `json:"url"`
	Keys []Key  `json:"keys"`
}

type Key struct {
	Kid         string `json:"kid"`
	SPKIDERFile string `json:"spki_der_file"`
	SHA256      string `json:"sha256"`
}

type ReleaseSource struct {
	Environment   string
	ServiceCAPEM  []byte
	Outputs       map[string]string
	ActiveSPKIDER []byte
}

// InputFromAWSRelease converts the public outputs of one deployed AWS Gateway
// stack into a release Contract input. It never accesses private key material.
func InputFromAWSRelease(source ReleaseSource) (Input, error) {
	value := func(name string) (string, error) {
		result := strings.TrimSpace(source.Outputs[name])
		if result == "" {
			return "", fmt.Errorf("AWS release source is missing %s", name)
		}
		return result, nil
	}
	gatewayURL, err := value("APIBaseURL")
	if err != nil {
		return Input{}, err
	}
	timeBootstrapURL, err := value("TimeBootstrapURL")
	if err != nil {
		return Input{}, err
	}
	keyID, err := value("TimeBootstrapKeyID")
	if err != nil {
		return Input{}, err
	}
	if _, err := value("TimeBootstrapKeyArn"); err != nil {
		return Input{}, err
	}
	input := Input{
		Environment:      strings.TrimSpace(source.Environment),
		ServiceBaseURL:   gatewayURL,
		ServiceCAPEM:     source.ServiceCAPEM,
		TimeBootstrapURL: timeBootstrapURL,
		SignerBackend:    "aws-kms",
		Keys:             []KeyInput{{Kid: keyID, SPKIDER: source.ActiveSPKIDER}},
	}
	if _, err := build(input); err != nil {
		return Input{}, err
	}
	return input, nil
}

// Write validates input and atomically creates a new contract directory.
func Write(directory string, input Input) (Contract, error) {
	contract, err := build(input)
	if err != nil {
		return Contract{}, err
	}
	if _, err := os.Lstat(directory); err == nil {
		return Contract{}, fmt.Errorf("contract output already exists: %s", directory)
	} else if !os.IsNotExist(err) {
		return Contract{}, fmt.Errorf("inspect contract output: %w", err)
	}
	parent := filepath.Dir(directory)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return Contract{}, fmt.Errorf("create contract parent: %w", err)
	}
	temporary, err := os.MkdirTemp(parent, ".device-bootstrap-contract-")
	if err != nil {
		return Contract{}, fmt.Errorf("create temporary contract: %w", err)
	}
	defer os.RemoveAll(temporary)
	if err := writeContractFiles(temporary, contract, input); err != nil {
		return Contract{}, err
	}
	if err := os.Rename(temporary, directory); err != nil {
		return Contract{}, fmt.Errorf("publish contract: %w", err)
	}
	return contract, nil
}

// Load verifies every contract file and returns its canonical metadata.
func Load(directory string) (Contract, error) {
	raw, err := readRegularFile(directory, contractFile)
	if err != nil {
		return Contract{}, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var contract Contract
	if err := decoder.Decode(&contract); err != nil {
		return Contract{}, fmt.Errorf("decode contract: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Contract{}, fmt.Errorf("contract has trailing JSON values")
	}
	input := Input{
		Environment:      contract.Environment,
		ServiceBaseURL:   contract.Service.BaseURL,
		TimeBootstrapURL: contract.TimeBootstrap.URL,
		SignerBackend:    "aws-kms",
	}
	if contract.Environment == "development" {
		input.SignerBackend = "local"
	}
	if contract.Service.CAPEMFile != "" {
		ca, err := readRegularFile(directory, contract.Service.CAPEMFile)
		if err != nil {
			return Contract{}, err
		}
		if sha256Hex(ca) != contract.Service.CAPEMSHA256 {
			return Contract{}, fmt.Errorf("service CA SHA-256 does not match contract")
		}
		input.ServiceCAPEM = ca
	}
	for _, key := range contract.TimeBootstrap.Keys {
		spki, err := readRegularFile(directory, key.SPKIDERFile)
		if err != nil {
			return Contract{}, err
		}
		if sha256Hex(spki) != key.SHA256 {
			return Contract{}, fmt.Errorf("time bootstrap SPKI SHA-256 does not match contract for %s", key.Kid)
		}
		input.Keys = append(input.Keys, KeyInput{Kid: key.Kid, SPKIDER: spki})
	}
	expected, err := build(input)
	if err != nil {
		return Contract{}, err
	}
	encoded, _ := json.Marshal(contract)
	expectedEncoded, _ := json.Marshal(expected)
	if string(encoded) != string(expectedEncoded) {
		return Contract{}, fmt.Errorf("contract metadata is not canonical")
	}
	return contract, nil
}

func build(input Input) (Contract, error) {
	input.Environment = strings.TrimSpace(input.Environment)
	input.ServiceBaseURL = strings.TrimSpace(input.ServiceBaseURL)
	input.TimeBootstrapURL = strings.TrimSpace(input.TimeBootstrapURL)
	input.SignerBackend = strings.TrimSpace(input.SignerBackend)
	if input.Environment != "development" && input.Environment != "staging" && input.Environment != "production" {
		return Contract{}, fmt.Errorf("contract environment must be development, staging, or production")
	}
	if input.SignerBackend == "local" && input.Environment != "development" {
		return Contract{}, fmt.Errorf("local signer is allowed only for development contracts")
	}
	if input.SignerBackend == "aws-kms" && input.Environment == "development" {
		return Contract{}, fmt.Errorf("aws-kms signer is not allowed for development contracts")
	}
	if input.SignerBackend != "local" && input.SignerBackend != "aws-kms" {
		return Contract{}, fmt.Errorf("contract signer backend must be local or aws-kms")
	}
	serviceURL, err := parseServiceURL(input.ServiceBaseURL)
	if err != nil {
		return Contract{}, err
	}
	if input.Environment != "development" && serviceURL.Scheme != "https" {
		return Contract{}, fmt.Errorf("staging and production service URLs must use HTTPS")
	}
	if serviceURL.Scheme == "https" {
		if err := validatePEMCertificates(input.ServiceCAPEM); err != nil {
			return Contract{}, fmt.Errorf("service CA: %w", err)
		}
	} else if len(input.ServiceCAPEM) != 0 {
		return Contract{}, fmt.Errorf("HTTP service contract must not include a CA")
	}
	if err := validateTimeBootstrapURL(input.TimeBootstrapURL); err != nil {
		return Contract{}, err
	}
	if len(input.Keys) == 0 || len(input.Keys) > 2 {
		return Contract{}, fmt.Errorf("contract requires one or two time bootstrap keys")
	}
	contract := Contract{
		SchemaVersion: SchemaVersion,
		Kind:          "device-service-bootstrap",
		Environment:   input.Environment,
		Service:       Service{BaseURL: input.ServiceBaseURL},
		TimeBootstrap: TimeBootstrap{URL: input.TimeBootstrapURL},
	}
	if serviceURL.Scheme == "https" {
		contract.Service.CAPEMFile = "service-ca.pem"
		contract.Service.CAPEMSHA256 = sha256Hex(input.ServiceCAPEM)
	}
	seenKids := make(map[string]struct{}, len(input.Keys))
	for _, key := range input.Keys {
		if !keyIDPattern.MatchString(key.Kid) {
			return Contract{}, fmt.Errorf("time bootstrap key id is invalid")
		}
		if _, exists := seenKids[key.Kid]; exists {
			return Contract{}, fmt.Errorf("time bootstrap key ids must be unique")
		}
		seenKids[key.Kid] = struct{}{}
		if err := validateP256SPKI(key.SPKIDER); err != nil {
			return Contract{}, fmt.Errorf("time bootstrap key %s: %w", key.Kid, err)
		}
		contract.TimeBootstrap.Keys = append(contract.TimeBootstrap.Keys, Key{
			Kid:         key.Kid,
			SPKIDERFile: filepath.ToSlash(filepath.Join("time-bootstrap", key.Kid+".der")),
			SHA256:      sha256Hex(key.SPKIDER),
		})
	}
	return contract, nil
}

func writeContractFiles(directory string, contract Contract, input Input) error {
	if contract.Service.CAPEMFile != "" {
		if err := os.WriteFile(filepath.Join(directory, contract.Service.CAPEMFile), input.ServiceCAPEM, 0o444); err != nil {
			return fmt.Errorf("write service CA: %w", err)
		}
	}
	for index, key := range contract.TimeBootstrap.Keys {
		path := filepath.Join(directory, filepath.FromSlash(key.SPKIDERFile))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return fmt.Errorf("create time bootstrap key directory: %w", err)
		}
		if err := os.WriteFile(path, input.Keys[index].SPKIDER, 0o444); err != nil {
			return fmt.Errorf("write time bootstrap SPKI: %w", err)
		}
	}
	encoded, err := json.MarshalIndent(contract, "", "  ")
	if err != nil {
		return fmt.Errorf("encode contract: %w", err)
	}
	if err := os.WriteFile(filepath.Join(directory, contractFile), append(encoded, '\n'), 0o444); err != nil {
		return fmt.Errorf("write contract: %w", err)
	}
	return nil
}

func parseServiceURL(value string) (*url.URL, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") || (parsed.Path != "" && parsed.Path != "/") {
		return nil, fmt.Errorf("service base URL must be an HTTP(S) origin without path, credentials, query, or fragment")
	}
	return parsed, nil
}

func validateTimeBootstrapURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" ||
		parsed.Fragment != "" || parsed.Path != "/api/v1/device-time/bootstrap" {
		return fmt.Errorf("time bootstrap URL must be the exact HTTP /api/v1/device-time/bootstrap endpoint")
	}
	return nil
}

func validatePEMCertificates(value []byte) error {
	if len(value) == 0 {
		return fmt.Errorf("is required for HTTPS")
	}
	remainder := value
	count := 0
	for {
		block, remaining := pem.Decode(remainder)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			return fmt.Errorf("contains a non-certificate PEM block")
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return fmt.Errorf("contains an invalid certificate: %w", err)
		}
		count++
		remainder = remaining
	}
	if count == 0 || len(strings.TrimSpace(string(remainder))) != 0 {
		return fmt.Errorf("must contain only PEM certificates")
	}
	return nil
}

func validateP256SPKI(value []byte) error {
	publicKey, err := x509.ParsePKIXPublicKey(value)
	if err != nil {
		return fmt.Errorf("must be a DER SPKI public key: %w", err)
	}
	key, ok := publicKey.(*ecdsa.PublicKey)
	if !ok || key.Curve != elliptic.P256() || len(value) != 91 {
		return fmt.Errorf("must be an uncompressed P-256 DER SPKI public key")
	}
	return nil
}

func readRegularFile(root, relative string) ([]byte, error) {
	if relative == "" || filepath.IsAbs(relative) || filepath.Clean(relative) != relative || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("contract references an unsafe path")
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return nil, fmt.Errorf("inspect contract root: %w", err)
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("contract root must be a non-symlink directory")
	}
	path := root
	components := strings.Split(relative, string(filepath.Separator))
	for index, component := range components {
		path = filepath.Join(path, component)
		info, err := os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("read contract file %s: %w", relative, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("contract file %s must not traverse a symlink", relative)
		}
		if index < len(components)-1 && !info.IsDir() {
			return nil, fmt.Errorf("contract path component %s must be a directory", component)
		}
		if index == len(components)-1 && !info.Mode().IsRegular() {
			return nil, fmt.Errorf("contract file %s must be regular", relative)
		}
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read contract file %s: %w", relative, err)
	}
	return contents, nil
}

func sha256Hex(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}
