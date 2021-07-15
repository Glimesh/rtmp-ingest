package glimesh

import (
	"context"
	"fmt"
	"github.com/clone1018/rtmp-ingest/pkg/services"
	"github.com/hasura/go-graphql-client"
	"golang.org/x/oauth2/clientcredentials"
	"strconv"
)

type Service struct {
	tokenUrl string
	apiUrl   string

	client *graphql.Client
	config *Config
}

type Config struct {
	Address      string
	ClientID     string
	ClientSecret string
}

func New(config Config) *Service {
	return &Service{
		tokenUrl: "/api/oauth/token",
		apiUrl:   "/api/graph",
		config:   &config,
	}
}

func (s *Service) Name() string {
	return "Glimesh"
}

func (s *Service) Connect() error {
	config := clientcredentials.Config{
		ClientID:     s.config.ClientID,
		ClientSecret: s.config.ClientSecret,
		TokenURL:     fmt.Sprintf("%s%s", s.config.Address, s.tokenUrl),
		Scopes:       []string{"streamkey"},
	}
	httpClient := config.Client(context.Background())
	s.client = graphql.NewClient(fmt.Sprintf("%s%s", s.config.Address, s.apiUrl), httpClient)

	return nil
}

func (s *Service) GetHmacKey(channelID uint32) ([]byte, error) {
	var hmacQuery struct {
		Channel struct {
			HmacKey graphql.String
		} `graphql:"channel(id: $id)"`
	}
	err := s.client.Query(context.Background(), &hmacQuery, map[string]interface{}{
		"id": graphql.ID(channelID),
	})
	if err != nil {
		return []byte{}, err
	}
	return []byte(hmacQuery.Channel.HmacKey), nil
}

func (s *Service) StartStream(channelID uint32) (uint32, error) {
	var startStreamMutation struct {
		Stream struct {
			Id graphql.String
		} `graphql:"startStream(channelId: $id)"`
	}
	err := s.client.Mutate(context.Background(), &startStreamMutation, map[string]interface{}{
		"id": graphql.ID(channelID),
	})
	if err != nil {
		return 0, err
	}

	id, err := strconv.Atoi(string(startStreamMutation.Stream.Id))
	if err != nil {
		return 0, err
	}
	return uint32(id), nil
}

func (s *Service) EndStream(streamID uint32) error {
	return nil
}

func (s *Service) UpdateStreamMetadata(streamID uint32, metadata services.StreamMetadata) error {
	return nil
}

func (s *Service) SendJpegPreviewImage(streamID uint32) error {
	return nil
}
