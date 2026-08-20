package awsiot

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	mqtt "github.com/eclipse/paho.mqtt.golang"
)

const (
	directACKConnectTimeout = 10 * time.Second
	directACKRetryDelay     = time.Second
	directACKURLTTL         = 15 * time.Minute
	emptySHA256             = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

var (
	iotDataEndpointPattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{1,253}\.iot\.[A-Za-z0-9-]+\.amazonaws\.com$`)
	directACKTopicPrefixPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

type DirectACKMessageHandler interface {
	HandleMQTT(context.Context, string, []byte) error
}

type DirectACKSubscriberConfig struct {
	Endpoint    string
	Region      string
	TopicPrefix string
	Credentials aws.CredentialsProvider
	Handler     DirectACKMessageHandler
	Logger      *log.Logger
}

type DirectACKSubscriber struct {
	config DirectACKSubscriberConfig
	logger *log.Logger
	now    func() time.Time

	mutex  sync.Mutex
	cancel context.CancelFunc
	client mqtt.Client
	wait   sync.WaitGroup
}

func NewDirectACKSubscriber(config DirectACKSubscriberConfig) (*DirectACKSubscriber, error) {
	if !iotDataEndpointPattern.MatchString(config.Endpoint) || config.Region == "" ||
		!validDirectACKTopicPrefix(config.TopicPrefix) || config.Credentials == nil || config.Handler == nil {
		return nil, fmt.Errorf("direct ACK subscriber requires an IoT endpoint, region, topic prefix, credentials, and handler")
	}
	logger := config.Logger
	if logger == nil {
		logger = log.Default()
	}
	return &DirectACKSubscriber{config: config, logger: logger, now: time.Now}, nil
}

func (subscriber *DirectACKSubscriber) Start(ctx context.Context) error {
	if subscriber == nil || ctx == nil {
		return fmt.Errorf("direct ACK subscriber is not configured")
	}
	subscriber.mutex.Lock()
	if subscriber.cancel != nil {
		subscriber.mutex.Unlock()
		return fmt.Errorf("direct ACK subscriber is already started")
	}
	runContext, cancel := context.WithCancel(ctx)
	subscriber.cancel = cancel
	subscriber.mutex.Unlock()

	client, lost, err := subscriber.connect(runContext)
	if err != nil {
		cancel()
		subscriber.mutex.Lock()
		subscriber.cancel = nil
		subscriber.mutex.Unlock()
		return err
	}
	subscriber.setClient(client)
	subscriber.wait.Add(1)
	go subscriber.maintain(runContext, client, lost)
	return nil
}

func (subscriber *DirectACKSubscriber) Close() {
	if subscriber == nil {
		return
	}
	subscriber.mutex.Lock()
	cancel := subscriber.cancel
	client := subscriber.client
	subscriber.cancel = nil
	subscriber.client = nil
	subscriber.mutex.Unlock()
	if cancel != nil {
		cancel()
	}
	if client != nil && client.IsConnectionOpen() {
		client.Disconnect(250)
	}
	subscriber.wait.Wait()
}

func (subscriber *DirectACKSubscriber) maintain(ctx context.Context, client mqtt.Client, lost <-chan error) {
	defer subscriber.wait.Done()
	for {
		select {
		case <-ctx.Done():
			if client.IsConnectionOpen() {
				client.Disconnect(250)
			}
			return
		case err := <-lost:
			if ctx.Err() != nil {
				return
			}
			subscriber.logger.Printf("direct MQTT ACK subscriber disconnected: %v", err)
		}

		for ctx.Err() == nil {
			timer := time.NewTimer(directACKRetryDelay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			reconnected, nextLost, err := subscriber.connect(ctx)
			if err != nil {
				subscriber.logger.Printf("direct MQTT ACK subscriber reconnect failed: %v", err)
				continue
			}
			client, lost = reconnected, nextLost
			subscriber.setClient(client)
			break
		}
	}
}

func (subscriber *DirectACKSubscriber) connect(ctx context.Context) (mqtt.Client, <-chan error, error) {
	credentials, err := subscriber.config.Credentials.Retrieve(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("retrieve AWS credentials for direct ACK subscriber: %w", err)
	}
	signedURL, err := presignIoTWebSocketURL(
		ctx, subscriber.config.Endpoint, subscriber.config.Region, credentials, subscriber.now().UTC(),
	)
	if err != nil {
		return nil, nil, err
	}
	clientID, err := directACKClientID()
	if err != nil {
		return nil, nil, err
	}
	lost := make(chan error, 1)
	options := mqtt.NewClientOptions().
		AddBroker(signedURL).
		SetClientID(clientID).
		SetProtocolVersion(4).
		SetCleanSession(true).
		SetAutoReconnect(false).
		SetConnectRetry(false).
		SetConnectTimeout(directACKConnectTimeout).
		SetWriteTimeout(directACKConnectTimeout).
		SetKeepAlive(30 * time.Second).
		SetPingTimeout(10 * time.Second).
		SetOrderMatters(false)
	options.SetConnectionLostHandler(func(_ mqtt.Client, connectionErr error) {
		select {
		case lost <- connectionErr:
		default:
		}
	})
	options.SetDefaultPublishHandler(func(_ mqtt.Client, message mqtt.Message) {
		payload := append([]byte(nil), message.Payload()...)
		if err := subscriber.config.Handler.HandleMQTT(ctx, message.Topic(), payload); err != nil {
			subscriber.logger.Printf("direct MQTT ingest rejected topic=%s: %v", message.Topic(), err)
		}
	})

	client := mqtt.NewClient(options)
	connectToken := client.Connect()
	if !connectToken.WaitTimeout(directACKConnectTimeout) {
		client.Disconnect(0)
		return nil, nil, fmt.Errorf("connect direct MQTT ACK subscriber: timeout")
	}
	if err := connectToken.Error(); err != nil {
		return nil, nil, fmt.Errorf("connect direct MQTT ACK subscriber: %w", err)
	}
	topics := directIngestTopics(subscriber.config.TopicPrefix)
	filters := make(map[string]byte, len(topics))
	for _, topic := range topics {
		filters[topic] = 1
	}
	subscribeToken := client.SubscribeMultiple(filters, nil)
	if !subscribeToken.WaitTimeout(directACKConnectTimeout) {
		client.Disconnect(250)
		return nil, nil, fmt.Errorf("subscribe direct MQTT ingest topics: timeout")
	}
	if err := subscribeToken.Error(); err != nil {
		client.Disconnect(250)
		return nil, nil, fmt.Errorf("subscribe direct MQTT ingest topics: %w", err)
	}
	subscriber.logger.Printf("direct MQTT ingest subscriber connected topics=%s", strings.Join(topics, ","))
	return client, lost, nil
}

func (subscriber *DirectACKSubscriber) setClient(client mqtt.Client) {
	subscriber.mutex.Lock()
	subscriber.client = client
	subscriber.mutex.Unlock()
}

func directACKTopic(topicPrefix string) string {
	return "cmd/" + topicPrefix + "/+/res"
}

func directIngestTopics(topicPrefix string) []string {
	return []string{
		directACKTopic(topicPrefix),
		"dt/" + topicPrefix + "/+/status",
		"dt/" + topicPrefix + "/+/lwt",
	}
}

func validDirectACKTopicPrefix(topicPrefix string) bool {
	return directACKTopicPrefixPattern.MatchString(topicPrefix)
}

func directACKClientID() (string, error) {
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate direct ACK subscriber client id: %w", err)
	}
	return "aws-iot-gateway-local-" + hex.EncodeToString(random[:]), nil
}

func presignIoTWebSocketURL(
	ctx context.Context,
	endpoint string,
	region string,
	credentials aws.Credentials,
	signingTime time.Time,
) (string, error) {
	if !iotDataEndpointPattern.MatchString(endpoint) || region == "" || !credentials.HasKeys() {
		return "", fmt.Errorf("invalid direct ACK WebSocket signing input")
	}
	requestURL := &url.URL{Scheme: "https", Host: endpoint, Path: "/mqtt"}
	query := requestURL.Query()
	query.Set("X-Amz-Expires", strconv.FormatInt(int64(directACKURLTTL/time.Second), 10))
	requestURL.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return "", fmt.Errorf("build direct ACK WebSocket request: %w", err)
	}
	signedURI, _, err := v4.NewSigner().PresignHTTP(
		ctx, credentials, request, emptySHA256, "iotdevicegateway", region, signingTime,
		func(options *v4.SignerOptions) { options.DisableSessionToken = true },
	)
	if err != nil {
		return "", fmt.Errorf("sign direct ACK WebSocket request: %w", err)
	}
	signedURL, err := url.Parse(signedURI)
	if err != nil {
		return "", fmt.Errorf("parse direct ACK WebSocket URL: %w", err)
	}
	if credentials.SessionToken != "" {
		query := signedURL.Query()
		query.Set("X-Amz-Security-Token", credentials.SessionToken)
		signedURL.RawQuery = query.Encode()
	}
	signedURL.Scheme = "wss"
	return signedURL.String(), nil
}
