package main

import (
	"context"
	"log"
	"net/http"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/iot"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/activation"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/awsiot"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/httpapi"
	gatewayruntime "gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/runtime"
)

func main() {
	config, err := gatewayruntime.LoadConfigFromEnvironment()
	if err != nil {
		log.Fatalf("invalid gateway configuration: %v", err)
	}
	awsConfig, err := awsconfig.LoadDefaultConfig(context.Background(), awsconfig.WithRegion(config.AWSRegion))
	if err != nil {
		log.Fatalf("load AWS configuration: %v", err)
	}
	provisioner, err := awsiot.New(
		iot.NewFromConfig(awsConfig),
		config.ThingPrefix,
		config.DevicePolicyName,
	)
	if err != nil {
		log.Fatalf("configure AWS IoT provisioner: %v", err)
	}
	service, err := activation.NewServiceWithStore(
		provisioner,
		activation.NewFileStore(config.ActivationState),
	)
	if err != nil {
		log.Fatalf("load activation state: %v", err)
	}
	server := &http.Server{
		Addr:              config.ListenAddress,
		Handler:           httpapi.New(service, httpapi.StaticTokenAuthenticator(config.InternalTokens)),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	log.Printf("aws-iot-gateway listening on %s", config.ListenAddress)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("serve gateway: %v", err)
	}
}
