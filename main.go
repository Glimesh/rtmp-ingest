package main

import (
	"fmt"
	"github.com/clone1018/rtmp-ingest/pkg/orchestrator"
	"github.com/clone1018/rtmp-ingest/pkg/protocols/ftl"
	"github.com/clone1018/rtmp-ingest/pkg/services/glimesh"
	"log"
	"net"
	"os"
)

// Lord forgive me, for I have globalled.
var streamManager StreamManager

func main() {
	hostname, err := os.Hostname()
	if err != nil {
		// How tf
		log.Fatalln(err)
	}

	// Should use viper or something in the future
	glimeshService := glimesh.New(glimesh.Config{
		Address:      os.Getenv("RTMP_INGEST_GLIMESH_ADDRESS"),
		ClientID:     os.Getenv("RTMP_INGEST_GLIMESH_CLIENT_ID"),
		ClientSecret: os.Getenv("RTMP_INGEST_GLIMESH_CLIENT_SECRET"),
	})
	err = glimeshService.Connect()
	if err != nil {
		log.Fatalln(err)
	}

	streamManager = NewStreamManager()

	orch := &orchestrator.Connection{
		OnStreamRelaying: func(message orchestrator.StreamRelayingMessage) {
			stream, err := streamManager.GetStream(message.ChannelID)
			if err != nil {
				panic(err)
			}

			ftlAddr := fmt.Sprintf("%s:%d", message.TargetHostname, ftl.DefaultPort)
			client, err := ftl.Dial(ftlAddr, message.ChannelID, message.StreamKey)
			if err != nil {
				panic(err)
			}

			mediaAddr := fmt.Sprintf("linux-dev:%d", client.AssignedMediaPort)
			mediaConn, err := net.Dial("udp", mediaAddr)
			if err != nil {
				panic(err)
			}
			log.Printf("[FTL Media] Established connection to %s \n", mediaAddr)
			log.Printf("[FTL Media] Remote UDP address : %s \n", mediaConn.RemoteAddr().String())
			log.Printf("[FTL Media] Local UDP client address : %s \n", mediaConn.LocalAddr().String())

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
		},
	}
	err = orch.Connect(os.Getenv("RTMP_INGEST_ORCHESTRATOR_ADDRESS"), "global", hostname)
	if err != nil {
		log.Fatalln(err)
	}


	go NewRTMPServer(glimeshService, orch)

	// Never stop stopping.
	select {}
}
