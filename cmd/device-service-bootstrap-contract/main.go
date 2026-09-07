// device-service-bootstrap-contract creates public provider-neutral bootstrap Contracts.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"flag"
	"fmt"
	"log"
	"os"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/devicebootstrap"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/localsigner"
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: device-service-bootstrap-contract local|aws-release [flags]")
	}
	switch args[0] {
	case "local":
		return runLocal(args[1:])
	case "aws-release":
		return runAWSRelease(ctx, args[1:])
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runLocal(args []string) error {
	flags := flag.NewFlagSet("local", flag.ContinueOnError)
	output := flags.String("out", "", "new contract output directory")
	serviceURL := flags.String("service-url", "", "development service origin")
	timeBootstrapURL := flags.String("time-bootstrap-url", "", "development time bootstrap URL")
	keyID := flags.String("key-id", "", "development time key id")
	privateKeyPath := flags.String("private-key", "", "owner-only P-256 PKCS#8 PEM private key")
	publicKeyPath := flags.String("public-key", "", "current P-256 DER SPKI exported by the signer owner")
	nextKeyID := flags.String("next-key-id", "", "optional next time key id during a planned rotation")
	nextPublicKeyPath := flags.String("next-public-key", "", "optional next P-256 DER SPKI during a planned rotation")
	serviceCAPath := flags.String("service-ca", "", "optional CA PEM for a local HTTPS service")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *output == "" || *serviceURL == "" || *timeBootstrapURL == "" || *keyID == "" {
		return fmt.Errorf("local requires --out, --service-url, --time-bootstrap-url, and --key-id")
	}
	if (*privateKeyPath == "") == (*publicKeyPath == "") {
		return fmt.Errorf("local requires exactly one of --private-key or --public-key")
	}
	if (*nextKeyID == "") != (*nextPublicKeyPath == "") {
		return fmt.Errorf("local rotation requires both --next-key-id and --next-public-key")
	}
	spki, err := localCurrentSPKI(*privateKeyPath, *publicKeyPath)
	if err != nil {
		return err
	}
	ca, err := readOptionalFile(*serviceCAPath)
	if err != nil {
		return err
	}
	keys := []devicebootstrap.KeyInput{{Kid: *keyID, SPKIDER: spki}}
	if *nextKeyID != "" {
		nextSPKI, err := readOptionalFile(*nextPublicKeyPath)
		if err != nil {
			return err
		}
		keys = append(keys, devicebootstrap.KeyInput{Kid: *nextKeyID, SPKIDER: nextSPKI})
	}
	_, err = devicebootstrap.Write(*output, devicebootstrap.Input{
		Environment:      "development",
		ServiceBaseURL:   *serviceURL,
		ServiceCAPEM:     ca,
		TimeBootstrapURL: *timeBootstrapURL,
		SignerBackend:    "local",
		Keys:             keys,
	})
	return err
}

func localCurrentSPKI(privateKeyPath, publicKeyPath string) ([]byte, error) {
	if publicKeyPath != "" {
		spki, err := os.ReadFile(publicKeyPath)
		if err != nil {
			return nil, fmt.Errorf("read local time signer public key: %w", err)
		}
		parsed, err := x509.ParsePKIXPublicKey(spki)
		if err != nil {
			return nil, fmt.Errorf("parse local time signer public key: %w", err)
		}
		publicKey, ok := parsed.(*ecdsa.PublicKey)
		if !ok || publicKey.Curve != elliptic.P256() {
			return nil, fmt.Errorf("local time signer public key must be P-256 DER SPKI")
		}
		return spki, nil
	}
	signer, err := localsigner.Load(privateKeyPath)
	if err != nil {
		return nil, err
	}
	publicKey, ok := signer.Public().(*ecdsa.PublicKey)
	if !ok || publicKey.Curve != elliptic.P256() {
		return nil, fmt.Errorf("local time signer did not expose a P-256 public key")
	}
	spki, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		return nil, fmt.Errorf("encode local time signer public key: %w", err)
	}
	return spki, nil
}

func runAWSRelease(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("aws-release", flag.ContinueOnError)
	output := flags.String("out", "", "new contract output directory")
	environment := flags.String("environment", "", "staging or production")
	stackName := flags.String("stack", "", "CloudFormation stack name")
	region := flags.String("region", "", "AWS region")
	serviceCAPath := flags.String("service-ca", "", "approved service CA PEM")
	nextKeyID := flags.String("next-key-id", "", "optional next time key id during a planned rotation")
	nextKMSKeyID := flags.String("next-kms-key-id", "", "optional next KMS key id during a planned rotation")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *output == "" || *environment == "" || *stackName == "" || *region == "" || *serviceCAPath == "" {
		return fmt.Errorf("aws-release requires --out, --environment, --stack, --region, and --service-ca")
	}
	if (*nextKeyID == "") != (*nextKMSKeyID == "") {
		return fmt.Errorf("aws-release rotation requires both --next-key-id and --next-kms-key-id")
	}
	ca, err := readOptionalFile(*serviceCAPath)
	if err != nil {
		return err
	}
	awsConfig, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(*region))
	if err != nil {
		return fmt.Errorf("load AWS configuration: %w", err)
	}
	stack, err := cloudformation.NewFromConfig(awsConfig).DescribeStacks(ctx, &cloudformation.DescribeStacksInput{StackName: stackName})
	if err != nil {
		return fmt.Errorf("describe Gateway stack: %w", err)
	}
	if len(stack.Stacks) != 1 {
		return fmt.Errorf("Gateway stack lookup returned %d stacks", len(stack.Stacks))
	}
	outputs := make(map[string]string, len(stack.Stacks[0].Outputs))
	for _, output := range stack.Stacks[0].Outputs {
		if output.OutputKey != nil && output.OutputValue != nil {
			outputs[*output.OutputKey] = *output.OutputValue
		}
	}
	keyARN := outputs["TimeBootstrapKeyArn"]
	kmsClient := kms.NewFromConfig(awsConfig)
	publicKey, err := getKMSTimeBootstrapPublicKey(ctx, kmsClient, keyARN)
	if err != nil {
		return err
	}
	input, err := devicebootstrap.InputFromAWSRelease(devicebootstrap.ReleaseSource{
		Environment:   *environment,
		ServiceCAPEM:  ca,
		Outputs:       outputs,
		ActiveSPKIDER: publicKey,
	})
	if err != nil {
		return err
	}
	if *nextKeyID != "" {
		nextPublicKey, err := getKMSTimeBootstrapPublicKey(ctx, kmsClient, *nextKMSKeyID)
		if err != nil {
			return err
		}
		input.Keys = append(input.Keys, devicebootstrap.KeyInput{Kid: *nextKeyID, SPKIDER: nextPublicKey})
	}
	_, err = devicebootstrap.Write(*output, input)
	return err
}

type kmsPublicKeyClient interface {
	GetPublicKey(context.Context, *kms.GetPublicKeyInput, ...func(*kms.Options)) (*kms.GetPublicKeyOutput, error)
}

func getKMSTimeBootstrapPublicKey(ctx context.Context, client kmsPublicKeyClient, keyID string) ([]byte, error) {
	if keyID == "" {
		return nil, fmt.Errorf("Gateway stack is missing TimeBootstrapKeyArn")
	}
	publicKey, err := client.GetPublicKey(ctx, &kms.GetPublicKeyInput{KeyId: &keyID})
	if err != nil {
		return nil, fmt.Errorf("get time bootstrap public key: %w", err)
	}
	if publicKey == nil || publicKey.KeySpec != types.KeySpecEccNistP256 ||
		publicKey.KeyUsage != types.KeyUsageTypeSignVerify || !supportsECDSASHA256(publicKey.SigningAlgorithms) {
		return nil, fmt.Errorf("time bootstrap KMS key must be ECC_NIST_P256 SIGN_VERIFY with ECDSA_SHA_256")
	}
	return publicKey.PublicKey, nil
}

func supportsECDSASHA256(algorithms []types.SigningAlgorithmSpec) bool {
	for _, algorithm := range algorithms {
		if algorithm == types.SigningAlgorithmSpecEcdsaSha256 {
			return true
		}
	}
	return false
}

func readOptionalFile(path string) ([]byte, error) {
	if path == "" {
		return nil, nil
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return contents, nil
}
