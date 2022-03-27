package glimesh

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/Glimesh/rtmp-ingest/pkg/protocols/ftl"
	"github.com/Glimesh/rtmp-ingest/pkg/services"
	"github.com/hasura/go-graphql-client"
	"golang.org/x/oauth2/clientcredentials"
)

type Service struct {
	tokenUrl string
	apiUrl   string

	client     *graphql.Client
	httpClient *http.Client
	config     *Config
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
	s.httpClient = config.Client(context.Background())
	s.client = graphql.NewClient(fmt.Sprintf("%s%s", s.config.Address, s.apiUrl), s.httpClient)

	return nil
}

func (s *Service) GetHmacKey(channelID ftl.ChannelID) ([]byte, error) {
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

func (s *Service) StartStream(channelID ftl.ChannelID) (ftl.StreamID, error) {
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
	return ftl.StreamID(id), nil
}

func (s *Service) EndStream(streamID ftl.StreamID) error {
	var endStreamMutation struct {
		Stream struct {
			Id graphql.String
		} `graphql:"endStream(streamId: $id)"`
	}
	return s.client.Mutate(context.Background(), &endStreamMutation, map[string]interface{}{
		"id": graphql.ID(streamID),
	})
}

func (s *Service) UpdateStreamMetadata(streamID ftl.StreamID, metadata services.StreamMetadata) error {
	return nil
}

func (s *Service) SendJpegPreviewImage(streamID ftl.StreamID, filename string) error {
	// Unfortunately hasura doesn't support this directly so we need to do a plain HTTP request
	query := `mutation {
		uploadStreamThumbnail(streamId: %d, thumbnail: "thumbdata") {
			id
		}
	}`

	f, err := os.Open(filename)
	if err != nil {
		return err
	}
	values := map[string]io.Reader{
		"thumbdata": f,
		"query":     strings.NewReader(fmt.Sprintf(query, streamID)),
	}

	return uploadMultipartRequest(s.httpClient, fmt.Sprintf("%s%s", s.config.Address, s.apiUrl), values)
}

func uploadMultipartRequest(client *http.Client, url string, values map[string]io.Reader) (err error) {
	// Prepare a form that you will submit to that URL.
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	for key, r := range values {
		var fw io.Writer
		if x, ok := r.(io.Closer); ok {
			defer x.Close()
		}
		// Add an image file
		if x, ok := r.(*os.File); ok {
			if fw, err = w.CreateFormFile(key, x.Name()); err != nil {
				return
			}
		} else {
			// Add other fields
			if fw, err = w.CreateFormField(key); err != nil {
				return
			}
		}
		if _, err = io.Copy(fw, r); err != nil {
			return err
		}

	}
	// Don't forget to close the multipart writer.
	// If you don't close it, your request will be missing the terminating boundary.
	w.Close()

	// Now that you have a form, you can submit it to your handler.
	req, err := http.NewRequest("POST", url, &b)
	if err != nil {
		return
	}
	// Don't forget to set the content type, this will contain the boundary.
	req.Header.Set("Content-Type", w.FormDataContentType())

	// Submit the request
	res, err := client.Do(req)
	if err != nil {
		return
	}

	// Check the response
	if res.StatusCode != http.StatusOK {
		err = fmt.Errorf("bad status: %s", res.Status)
	}
	return
}
