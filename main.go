package main

import (
	"fmt"
	"net"
	"os"
	"os/signal"

	"github.com/clone1018/rtmp-ingest/pkg/orchestrator"
	"github.com/clone1018/rtmp-ingest/pkg/protocols/ftl"
	"github.com/clone1018/rtmp-ingest/pkg/services/glimesh"

	"github.com/sirupsen/logrus"
)

// Lord forgive me, for I have globalled.
var (
	streamManager StreamManager
)

func main() {
	log := logrus.New()
	log.Level = logrus.DebugLevel

	hostname, err := os.Hostname()
	if err != nil {
		// How tf
		log.Fatal(err)
	}

	// Should use viper or something in the future
	glimeshService := glimesh.New(glimesh.Config{
		Address:      os.Getenv("RTMP_INGEST_GLIMESH_ADDRESS"),
		ClientID:     os.Getenv("RTMP_INGEST_GLIMESH_CLIENT_ID"),
		ClientSecret: os.Getenv("RTMP_INGEST_GLIMESH_CLIENT_SECRET"),
	})
	err = glimeshService.Connect()
	if err != nil {
		log.Fatal(err)
	}

	streamManager = NewStreamManager()

	orchTransport, err := net.Dial("tcp", os.Getenv("RTMP_INGEST_ORCHESTRATOR_ADDRESS"))
	if err != nil {
		log.Fatal(err)
	}
	orch := orchestrator.NewClient(&orchestrator.Config{
		RegionCode: "global",
		Hostname:   hostname,
		Logger:     log.WithFields(logrus.Fields{"app": "orchestrator"}),
		Callbacks: orchestrator.Callbacks{
			OnStreamRelaying: func(message orchestrator.StreamRelayingMessage) {
				// TODO: Where do we put this?
				stream, err := streamManager.GetStream(message.ChannelID)
				if err != nil {
					// Do nothing?
					log.WithField("channel_id", message.ChannelID).Error("Channel not found in StreamManager")
					return
				}

				if message.Context == 1 {
					// Request to relay
					ftlAddr := fmt.Sprintf("%s:%d", message.TargetHostname, ftl.DefaultPort)
					client, ftlErr := ftl.Dial(ftlAddr, message.ChannelID, message.StreamKey)
					if ftlErr != nil {
						log.WithField("channel_id", message.ChannelID).Error(ftlErr)
						return
					}

					mediaAddr := fmt.Sprintf("%s:%d", message.TargetHostname, client.AssignedMediaPort)
					mediaConn, err := net.Dial("udp", mediaAddr)
					if err != nil {
						log.WithField("channel_id", message.ChannelID).Error(err)
						return
					}

					// Read nothing
					go func() {
						buffer := make([]byte, 1024)
						_, err = mediaConn.Read(buffer)
						if err != nil {
							panic(err)
						}
					}()

					// setting the rtpWriter is enough to get a stream of packets coming through
					stream.rtpWriter = mediaConn
				} else {
					// Request to stop relay
					stream.rtpWriter = nil
					stream.OnClose()
				}
			},
		},
	})
	if err := orch.Connect(orchTransport); err != nil {
		log.Fatal(err)
	}
	closeHandler(orch)

	go NewRTMPServer(glimeshService, orch, log.WithFields(logrus.Fields{"app": "rtmp"}))

	// Never stop stopping.
	select {}
}

func closeHandler(orch *orchestrator.Client) {
	c := make(chan os.Signal)
	// Wonder if this should listen to os.Kill as well?
	signal.Notify(c, os.Interrupt)
	go func() {
		<-c

		orch.Close()

		os.Exit(0)
	}()
}
