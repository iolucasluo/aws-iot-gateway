package awskvs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/kinesisvideo"
	kvtypes "github.com/aws/aws-sdk-go-v2/service/kinesisvideo/types"
	"github.com/aws/aws-sdk-go-v2/service/kinesisvideosignaling"
)

var (
	ErrChannelNotFound = errors.New("KVS signaling channel not found")
	ErrUnavailable     = errors.New("KVS signaling unavailable")
	ErrSigningFailed   = errors.New("KVS signaling URL signing failed")
)

type ICEServer struct {
	URLs       any    `json:"urls"`
	Username   string `json:"username,omitempty"`
	Credential string `json:"credential,omitempty"`
}

type PlaybackConfig struct {
	Provider    string
	ChannelName string
	ChannelARN  string
	Region      string
	ClientID    string
	WSSURL      string
	ICEServers  []ICEServer
	ExpiresAt   time.Time
}

type Channel struct {
	Name string
	ARN  string
}

type Client struct {
	config aws.Config
	region string
	client *kinesisvideo.Client
	now    func() time.Time
}

func NewClient(config aws.Config, region string) (*Client, error) {
	region = strings.TrimSpace(region)
	if region == "" {
		region = config.Region
	}
	if region == "" {
		return nil, fmt.Errorf("AWS KVS region is required")
	}
	config.Region = region
	return &Client{config: config, region: region, client: kinesisvideo.NewFromConfig(config), now: time.Now}, nil
}

func (client *Client) EnsureChannel(ctx context.Context, channelName string) (Channel, error) {
	channelName = strings.TrimSpace(channelName)
	if channelName == "" {
		return Channel{}, ErrChannelNotFound
	}
	channel, err := client.describe(ctx, channelName)
	if errors.Is(err, ErrChannelNotFound) {
		if _, createErr := client.client.CreateSignalingChannel(ctx, &kinesisvideo.CreateSignalingChannelInput{
			ChannelName: aws.String(channelName),
		}); createErr != nil && !strings.Contains(strings.ToLower(createErr.Error()), "resourceinuse") {
			return Channel{}, fmt.Errorf("create KVS signaling channel: %w", createErr)
		}
		return client.describe(ctx, channelName)
	}
	return channel, err
}

func (client *Client) describe(ctx context.Context, channelName string) (Channel, error) {
	result, err := client.client.DescribeSignalingChannel(ctx, &kinesisvideo.DescribeSignalingChannelInput{
		ChannelName: aws.String(channelName),
	})
	if err != nil {
		lower := strings.ToLower(err.Error())
		if strings.Contains(lower, "notfound") || strings.Contains(lower, "not found") {
			return Channel{}, ErrChannelNotFound
		}
		return Channel{}, fmt.Errorf("describe KVS signaling channel: %w", err)
	}
	if result.ChannelInfo == nil || result.ChannelInfo.ChannelARN == nil {
		return Channel{}, ErrChannelNotFound
	}
	return Channel{Name: channelName, ARN: aws.ToString(result.ChannelInfo.ChannelARN)}, nil
}

func (client *Client) Playback(ctx context.Context, channel Channel, viewerClientID string, ttl time.Duration) (PlaybackConfig, error) {
	if channel.Name == "" || channel.ARN == "" || strings.TrimSpace(viewerClientID) == "" || ttl <= 0 || ttl > 15*time.Minute {
		return PlaybackConfig{}, ErrUnavailable
	}
	endpoints, err := client.endpoints(ctx, channel.ARN)
	if err != nil {
		return PlaybackConfig{}, err
	}
	iceServers, iceExpiry, err := client.iceServers(ctx, endpoints["HTTPS"], channel.ARN, viewerClientID)
	if err != nil {
		return PlaybackConfig{}, err
	}
	wssURL, wssExpiry, err := client.signWSS(ctx, endpoints["WSS"], channel.ARN, viewerClientID, ttl)
	if err != nil {
		return PlaybackConfig{}, fmt.Errorf("%w: %v", ErrSigningFailed, err)
	}
	expiresAt := wssExpiry
	if !iceExpiry.IsZero() && iceExpiry.Before(expiresAt) {
		expiresAt = iceExpiry
	}
	return PlaybackConfig{
		Provider: "aws_kvs", ChannelName: channel.Name, ChannelARN: channel.ARN,
		Region: client.region, ClientID: viewerClientID, WSSURL: wssURL,
		ICEServers: iceServers, ExpiresAt: expiresAt,
	}, nil
}

func (client *Client) endpoints(ctx context.Context, channelARN string) (map[string]string, error) {
	result, err := client.client.GetSignalingChannelEndpoint(ctx, &kinesisvideo.GetSignalingChannelEndpointInput{
		ChannelARN: aws.String(channelARN),
		SingleMasterChannelEndpointConfiguration: &kvtypes.SingleMasterChannelEndpointConfiguration{
			Protocols: []kvtypes.ChannelProtocol{kvtypes.ChannelProtocolWss, kvtypes.ChannelProtocolHttps},
			Role:      kvtypes.ChannelRoleViewer,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("get KVS signaling endpoints: %w", err)
	}
	values := make(map[string]string)
	for _, endpoint := range result.ResourceEndpointList {
		values[string(endpoint.Protocol)] = aws.ToString(endpoint.ResourceEndpoint)
	}
	if values["WSS"] == "" || values["HTTPS"] == "" {
		return nil, ErrUnavailable
	}
	return values, nil
}

func (client *Client) iceServers(ctx context.Context, endpoint, channelARN, viewerClientID string) ([]ICEServer, time.Time, error) {
	signaling := kinesisvideosignaling.NewFromConfig(client.config, func(options *kinesisvideosignaling.Options) {
		options.BaseEndpoint = aws.String(endpoint)
	})
	result, err := signaling.GetIceServerConfig(ctx, &kinesisvideosignaling.GetIceServerConfigInput{
		ChannelARN: aws.String(channelARN), ClientId: aws.String(viewerClientID),
	})
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("get KVS ICE servers: %w", err)
	}
	servers := []ICEServer{{URLs: "stun:stun.kinesisvideo." + client.region + ".amazonaws.com:443"}}
	var earliest time.Time
	now := client.now().UTC()
	for _, item := range result.IceServerList {
		urls := make([]string, 0, len(item.Uris))
		for _, uri := range item.Uris {
			if strings.TrimSpace(uri) != "" {
				urls = append(urls, strings.TrimSpace(uri))
			}
		}
		if len(urls) == 0 {
			continue
		}
		servers = append(servers, ICEServer{
			URLs: urls, Username: aws.ToString(item.Username), Credential: aws.ToString(item.Password),
		})
		if item.Ttl != nil && *item.Ttl > 0 {
			expiry := now.Add(time.Duration(*item.Ttl) * time.Second)
			if earliest.IsZero() || expiry.Before(earliest) {
				earliest = expiry
			}
		}
	}
	return servers, earliest, nil
}

func (client *Client) signWSS(ctx context.Context, endpoint, channelARN, viewerClientID string, ttl time.Duration) (string, time.Time, error) {
	credentials, err := client.config.Credentials.Retrieve(ctx)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("retrieve AWS credentials: %w", err)
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("parse KVS WSS endpoint: %w", err)
	}
	parsed.Scheme = "https"
	query := parsed.Query()
	query.Set("X-Amz-ChannelARN", channelARN)
	query.Set("X-Amz-ClientId", viewerClientID)
	query.Set("X-Amz-Expires", strconv.FormatInt(int64(ttl/time.Second), 10))
	parsed.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return "", time.Time{}, err
	}
	emptyHash := sha256.Sum256(nil)
	now := client.now().UTC()
	presignedURL, _, err := v4.NewSigner().PresignHTTP(
		ctx, credentials, request, hex.EncodeToString(emptyHash[:]), "kinesisvideo", client.region, now,
	)
	if err != nil {
		return "", time.Time{}, err
	}
	presigned, err := url.Parse(presignedURL)
	if err != nil {
		return "", time.Time{}, err
	}
	presigned.Scheme = "wss"
	return presigned.String(), now.Add(ttl), nil
}
