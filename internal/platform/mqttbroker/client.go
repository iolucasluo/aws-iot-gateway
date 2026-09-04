// Package mqttbroker implements the provider-neutral device messaging port
// over an MQTT broker with mandatory TLS client authentication.
package mqttbroker

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/provider"
)

const (
	defaultConnectTimeout = 10 * time.Second
	defaultKeepAlive      = 30 * time.Second
	defaultPingTimeout    = 10 * time.Second
)

var (
	clientIDPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	topicPrefixPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

// Config contains only non-secret connection shape and paths to operator-owned
// TLS material. The private key is read once at construction and is never
// logged or exposed through the provider port.
type Config struct {
	Endpoint       string
	ClientID       string
	TopicPrefix    string
	CAFile         string
	ClientCertFile string
	ClientKeyFile  string
	ServerName     string
	ConnectTimeout time.Duration
	KeepAlive      time.Duration
	PingTimeout    time.Duration
}

type Client struct {
	broker         mqtt.Client
	topicPrefix    string
	connectTimeout time.Duration
	mutex          sync.Mutex
}

var _ provider.DeviceMessaging = (*Client)(nil)

func NewClient(config Config) (*Client, error) {
	endpoint := strings.TrimSpace(config.Endpoint)
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "mqtts" || parsed.Host == "" || parsed.User != nil ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || strings.ContainsAny(endpoint, "\r\n") {
		return nil, fmt.Errorf("MQTT broker endpoint must be an mqtts URL without credentials, path, or query")
	}
	clientID := strings.TrimSpace(config.ClientID)
	if !clientIDPattern.MatchString(clientID) {
		return nil, fmt.Errorf("MQTT client ID is invalid")
	}
	topicPrefix := strings.TrimSpace(config.TopicPrefix)
	if !topicPrefixPattern.MatchString(topicPrefix) {
		return nil, fmt.Errorf("MQTT topic prefix is invalid")
	}
	for _, material := range []struct {
		name string
		path string
	}{
		{name: "CAFile", path: config.CAFile},
		{name: "ClientCertFile", path: config.ClientCertFile},
		{name: "ClientKeyFile", path: config.ClientKeyFile},
	} {
		if strings.TrimSpace(material.path) == "" || strings.ContainsAny(material.path, "\r\n") {
			return nil, fmt.Errorf("MQTT %s is required and invalid", material.name)
		}
	}
	tlsConfig, err := loadTLSConfig(config, parsed.Hostname())
	if err != nil {
		return nil, fmt.Errorf("configure MQTT TLS: %w", err)
	}
	connectTimeout := config.ConnectTimeout
	if connectTimeout <= 0 {
		connectTimeout = defaultConnectTimeout
	}
	keepAlive := config.KeepAlive
	if keepAlive <= 0 {
		keepAlive = defaultKeepAlive
	}
	pingTimeout := config.PingTimeout
	if pingTimeout <= 0 {
		pingTimeout = defaultPingTimeout
	}
	options := mqtt.NewClientOptions().
		AddBroker(endpoint).
		SetClientID(clientID).
		SetTLSConfig(tlsConfig).
		SetProtocolVersion(4).
		SetCleanSession(true).
		SetAutoReconnect(true).
		SetResumeSubs(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(time.Second).
		SetConnectTimeout(connectTimeout).
		SetWriteTimeout(connectTimeout).
		SetKeepAlive(keepAlive).
		SetPingTimeout(pingTimeout).
		SetOrderMatters(false)
	return &Client{broker: mqtt.NewClient(options), topicPrefix: topicPrefix, connectTimeout: connectTimeout}, nil
}

func loadTLSConfig(config Config, defaultServerName string) (*tls.Config, error) {
	caPEM, err := os.ReadFile(config.CAFile)
	if err != nil {
		return nil, fmt.Errorf("read CA file: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("CA file contains no valid certificate")
	}
	certificate, err := tls.LoadX509KeyPair(config.ClientCertFile, config.ClientKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load client certificate and key: %w", err)
	}
	serverName := strings.TrimSpace(config.ServerName)
	if serverName == "" {
		serverName = defaultServerName
	}
	if serverName == "" || strings.ContainsAny(serverName, "\r\n") {
		return nil, errors.New("TLS server name is invalid")
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		RootCAs:      roots,
		Certificates: []tls.Certificate{certificate},
		ServerName:   serverName,
	}, nil
}

func (client *Client) Connect(ctx context.Context) error {
	if client == nil || client.broker == nil || ctx == nil {
		return errors.New("MQTT broker client is not configured")
	}
	client.mutex.Lock()
	if client.broker.IsConnectionOpen() {
		client.mutex.Unlock()
		return nil
	}
	token := client.broker.Connect()
	client.mutex.Unlock()
	if err := waitToken(ctx, token, client.connectTimeout); err != nil {
		client.broker.Disconnect(0)
		return fmt.Errorf("connect MQTT broker: %w", err)
	}
	tracef("connected client topic_prefix=%s", client.topicPrefix)
	return nil
}

func (client *Client) Close() {
	if client == nil || client.broker == nil {
		return
	}
	client.broker.Disconnect(250)
}

func (client *Client) Publish(ctx context.Context, message provider.DeviceMessage) error {
	if client == nil || client.broker == nil || ctx == nil || !topicInNamespace(message.Topic, client.topicPrefix) {
		return errors.New("invalid MQTT publish message")
	}
	if message.ExpiresAt.IsZero() || !time.Now().UTC().Before(message.ExpiresAt) {
		return errors.New("MQTT publish message is expired")
	}
	if !client.broker.IsConnectionOpen() {
		return errors.New("MQTT broker is not connected")
	}
	token := client.broker.Publish(message.Topic, 1, false, message.Payload)
	if err := waitToken(ctx, token, client.connectTimeout); err != nil {
		return fmt.Errorf("publish MQTT message: %w", err)
	}
	return nil
}

func (client *Client) Subscribe(ctx context.Context, subscription provider.DeviceSubscription) (provider.SubscriptionHandle, error) {
	if client == nil || client.broker == nil || ctx == nil || subscription.Handler == nil ||
		!topicInNamespace(subscription.TopicFilter, client.topicPrefix) {
		return nil, errors.New("invalid MQTT subscription")
	}
	if !client.broker.IsConnectionOpen() {
		return nil, errors.New("MQTT broker is not connected")
	}
	token := client.broker.Subscribe(subscription.TopicFilter, 1, func(_ mqtt.Client, message mqtt.Message) {
		payload := append([]byte(nil), message.Payload()...)
		err := subscription.Handler(ctx, provider.DeviceMessage{Topic: message.Topic(), Payload: payload})
		tracef("message topic=%s bytes=%d handler_error=%v", message.Topic(), len(payload), err)
	})
	if err := waitToken(ctx, token, client.connectTimeout); err != nil {
		return nil, fmt.Errorf("subscribe MQTT topic: %w", err)
	}
	return &subscriptionHandle{client: client.broker, filter: subscription.TopicFilter}, nil
}

func tracef(format string, args ...any) {
	if os.Getenv("EXPLOREX_MQTT_TRACE") == "1" {
		log.Printf("mqttbroker: "+format, args...)
	}
}

func (client *Client) Health(ctx context.Context) error {
	return client.Connect(ctx)
}

type subscriptionHandle struct {
	client mqtt.Client
	filter string
	once   sync.Once
	err    error
}

func (handle *subscriptionHandle) Close() error {
	if handle == nil || handle.client == nil {
		return nil
	}
	handle.once.Do(func() {
		token := handle.client.Unsubscribe(handle.filter)
		if !token.WaitTimeout(defaultConnectTimeout) {
			handle.err = errors.New("unsubscribe MQTT topic: timeout")
			return
		}
		handle.err = token.Error()
	})
	return handle.err
}

func waitToken(ctx context.Context, token mqtt.Token, timeout time.Duration) error {
	if token == nil {
		return errors.New("MQTT operation returned no token")
	}
	done := make(chan bool, 1)
	go func() { done <- token.WaitTimeout(timeout) }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case completed := <-done:
		if !completed {
			return errors.New("timeout")
		}
		return token.Error()
	}
}

func topicInNamespace(topic, topicPrefix string) bool {
	if topic == "" || !topicPrefixPattern.MatchString(topicPrefix) {
		return false
	}
	parts := strings.Split(topic, "/")
	if len(parts) < 3 || parts[1] != topicPrefix {
		return false
	}
	for index, part := range parts {
		if part == "" || part == "." || part == ".." {
			return false
		}
		if part == "+" {
			continue
		}
		if part == "#" {
			if index != len(parts)-1 {
				return false
			}
			continue
		}
		if strings.ContainsAny(part, "+#") {
			return false
		}
	}
	return true
}
